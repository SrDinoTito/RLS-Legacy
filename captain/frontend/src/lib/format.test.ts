import { describe, expect, it } from 'bun:test';
import { fmtAlertMessage, fmtAlertReading } from './format';

describe('fmtAlertReading', () => {
	it('muestra las lecturas de CPU y RAM como porcentaje', () => {
		expect(fmtAlertReading('high_cpu', 94.6)).toBe('94.6%');
		expect(fmtAlertReading('high_ram', 82)).toBe('82.0%');
	});

	it('muestra la edad de una señal offline como duración', () => {
		expect(fmtAlertReading('offline', 125)).toBe('2m 5s sin señal');
	});

	it('protege lecturas nulas o corruptas', () => {
		expect(fmtAlertReading('high_cpu', null)).toBe('—');
		expect(fmtAlertReading('offline', Number.NaN)).toBe('—');
	});
});

describe('fmtAlertMessage', () => {
	it('corrige el texto histórico que decía sostenido para una sola muestra', () => {
		expect(fmtAlertMessage('CPU sostenido: valor 99.2 supera umbral 90')).toBe(
			'CPU alta: valor 99.2 supera umbral 90'
		);
		expect(fmtAlertMessage('RAM SOSTENIDO: valor 96 supera umbral 95')).toBe(
			'RAM alta: valor 96 supera umbral 95'
		);
	});

	it('tolera mensajes vacíos y conserva el resto del texto', () => {
		expect(fmtAlertMessage(null)).toBe('');
		expect(fmtAlertMessage('Sin comunicación')).toBe('Sin comunicación');
	});
});
