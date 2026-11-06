package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	NotificationScopeVPS   = "vps"
	NotificationScopeAgent = "agent"
	unknownVPSScopeID      = "sin-hostname"
)

// NotificationPolicy is the effective outbound policy for a VPS or agent.
// Inherited is true only when an agent has no explicit override and therefore
// follows its VPS policy. In-app alert records and SSE are never suppressed.
type NotificationPolicy struct {
	Scope                   string `json:"scope"`
	ScopeID                 string `json:"scope_id"`
	NotificationsEnabled    bool   `json:"notifications_enabled"`
	ResourceAlertsEnabled   bool   `json:"resource_alerts_enabled"`
	VPSNotificationsEnabled bool   `json:"vps_notifications_enabled"`
	Customized              bool   `json:"customized"`
	Inherited               bool   `json:"inherited"`
	ParentScopeID           string `json:"parent_scope_id,omitempty"`
}

type policyValues struct {
	notificationsEnabled  bool
	resourceAlertsEnabled bool
	customized            bool
}

func defaultPolicyValues() policyValues {
	return policyValues{notificationsEnabled: true, resourceAlertsEnabled: false}
}

// GetNotificationPolicy returns the effective policy. Agents inherit from
// their VPS (stable Docker Engine ID when present, hostname for older agents).
// Unconfigured VPSes default to general notices enabled and resource notices
// disabled, preventing CPU/RAM noise until explicitly opted in.
func (s *Store) GetNotificationPolicy(ctx context.Context, scope, scopeID string) (NotificationPolicy, error) {
	scopeID = strings.TrimSpace(scopeID)
	if scopeID == "" {
		return NotificationPolicy{}, fmt.Errorf("scope id required")
	}
	switch scope {
	case NotificationScopeVPS:
		values, err := s.policyValues(ctx, scope, scopeID)
		if err != nil {
			return NotificationPolicy{}, err
		}
		return NotificationPolicy{
			Scope: scope, ScopeID: scopeID,
			NotificationsEnabled:  values.notificationsEnabled,
			ResourceAlertsEnabled: values.resourceAlertsEnabled,
			Customized:            values.customized,
		}, nil
	case NotificationScopeAgent:
		id, err := strconv.ParseInt(scopeID, 10, 64)
		if err != nil || id <= 0 {
			return NotificationPolicy{}, fmt.Errorf("invalid agent id")
		}
		agent, err := s.AgentByID(ctx, id)
		if err != nil {
			return NotificationPolicy{}, err
		}
		vpsID := vpsScopeID(agent.Hostname, agent.Labels)
		parent, err := s.policyValues(ctx, NotificationScopeVPS, vpsID)
		if err != nil {
			return NotificationPolicy{}, err
		}
		agentValues, err := s.policyValues(ctx, scope, strconv.FormatInt(id, 10))
		if err != nil {
			return NotificationPolicy{}, err
		}
		values := agentValues
		if !agentValues.customized {
			values = parent
		}
		// A VPS-wide mute is a hard ceiling: no per-container override can
		// re-enable delivery while the parent VPS is muted. With the VPS
		// generally enabled, an agent override may opt into/out of CPU/RAM.
		notificationsEnabled := parent.notificationsEnabled && values.notificationsEnabled
		return NotificationPolicy{
			Scope: scope, ScopeID: strconv.FormatInt(id, 10),
			NotificationsEnabled:    notificationsEnabled,
			ResourceAlertsEnabled:   values.resourceAlertsEnabled,
			VPSNotificationsEnabled: parent.notificationsEnabled,
			Customized:              agentValues.customized,
			Inherited:               !agentValues.customized,
			ParentScopeID:           vpsID,
		}, nil
	default:
		return NotificationPolicy{}, fmt.Errorf("scope must be %q or %q", NotificationScopeVPS, NotificationScopeAgent)
	}
}

// SetNotificationPolicy stores an explicit policy. For a container, this
// overrides the current VPS defaults until DeleteNotificationPolicy is used.
func (s *Store) SetNotificationPolicy(ctx context.Context, scope, scopeID string, notificationsEnabled, resourceAlertsEnabled bool, updatedAt int64) error {
	scopeID = strings.TrimSpace(scopeID)
	if scopeID == "" {
		return fmt.Errorf("scope id required")
	}
	if scope == NotificationScopeAgent {
		id, err := strconv.ParseInt(scopeID, 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("invalid agent id")
		}
		if _, err := s.AgentByID(ctx, id); err != nil {
			return err
		}
		scopeID = strconv.FormatInt(id, 10)
	} else if scope != NotificationScopeVPS {
		return fmt.Errorf("scope must be %q or %q", NotificationScopeVPS, NotificationScopeAgent)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO notification_policies(scope, scope_id, notifications_enabled, resource_alerts_enabled, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(scope, scope_id) DO UPDATE SET
			notifications_enabled = excluded.notifications_enabled,
			resource_alerts_enabled = excluded.resource_alerts_enabled,
			updated_at = excluded.updated_at`, scope, scopeID, boolInt(notificationsEnabled), boolInt(resourceAlertsEnabled), updatedAt)
	if err != nil {
		return fmt.Errorf("save notification policy: %w", err)
	}
	return nil
}

// DeleteNotificationPolicy removes an explicit setting: a VPS returns to
// defaults, while an agent starts inheriting its VPS policy again.
func (s *Store) DeleteNotificationPolicy(ctx context.Context, scope, scopeID string) error {
	scopeID = strings.TrimSpace(scopeID)
	if scopeID == "" {
		return fmt.Errorf("scope id required")
	}
	if scope == NotificationScopeAgent {
		id, err := strconv.ParseInt(scopeID, 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("invalid agent id")
		}
		if _, err := s.AgentByID(ctx, id); err != nil {
			return err
		}
		scopeID = strconv.FormatInt(id, 10)
	} else if scope != NotificationScopeVPS {
		return fmt.Errorf("scope must be %q or %q", NotificationScopeVPS, NotificationScopeAgent)
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM notification_policies WHERE scope = ? AND scope_id = ?`, scope, scopeID)
	if err != nil {
		return fmt.Errorf("delete notification policy: %w", err)
	}
	return nil
}

// ShouldNotifyAlert is the final outbound-notification gate. Alerts remain
// stored and broadcast to the dashboard even when this returns false.
func (s *Store) ShouldNotifyAlert(ctx context.Context, agentID int64, alertType string) (bool, error) {
	policy, err := s.GetNotificationPolicy(ctx, NotificationScopeAgent, strconv.FormatInt(agentID, 10))
	if errors.Is(err, ErrNotFound) {
		return alertType != "high_cpu" && alertType != "high_ram", nil
	}
	if err != nil {
		return false, err
	}
	if !policy.NotificationsEnabled {
		return false, nil
	}
	if alertType == "high_cpu" || alertType == "high_ram" {
		return policy.ResourceAlertsEnabled, nil
	}
	return true, nil
}

func (s *Store) policyValues(ctx context.Context, scope, scopeID string) (policyValues, error) {
	var notificationsEnabled, resourceAlertsEnabled int
	err := s.db.QueryRowContext(ctx, `SELECT notifications_enabled, resource_alerts_enabled
		FROM notification_policies WHERE scope = ? AND scope_id = ?`, scope, scopeID).
		Scan(&notificationsEnabled, &resourceAlertsEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultPolicyValues(), nil
	}
	if err != nil {
		return policyValues{}, fmt.Errorf("read notification policy: %w", err)
	}
	return policyValues{
		notificationsEnabled:  notificationsEnabled != 0,
		resourceAlertsEnabled: resourceAlertsEnabled != 0,
		customized:            true,
	}, nil
}

func vpsScopeID(hostname string, labels sql.NullString) string {
	var values map[string]string
	if labels.Valid && json.Unmarshal([]byte(labels.String), &values) == nil {
		if id := strings.TrimSpace(values["rls_vps_id"]); id != "" {
			return id
		}
	}
	if hostname = strings.TrimSpace(hostname); hostname != "" {
		return hostname
	}
	return unknownVPSScopeID
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
