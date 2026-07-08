-- Seeds the UC2 typology catalog with the litis categories named in
-- plan.md §4 UC2. exemplar_ruling_ids is left empty — the archive is still
-- 100% untagged (plan.md §9 item 2), so there's no case_type-tagged ruling
-- yet to link as an exemplar; backfill once the grading pass happens.

INSERT INTO typologies (name, description, discriminating_features) VALUES
(
    'despido injustificado',
    'El trabajador alega haber sido separado de su empleo sin causa justificada y sin el procedimiento de rescisión previsto por la ley.',
    '["negación o admisión del despido por la parte patronal", "ausencia de aviso de rescisión por escrito", "reclamo de reinstalación o indemnización constitucional"]'
),
(
    'rescisión de contrato',
    'La parte patronal (o el trabajador) da por terminada la relación laboral invocando una causa específica prevista en la ley.',
    '["aviso de rescisión con causa y fecha", "referencia a fracción del artículo 47 o 51 de la Ley Federal del Trabajo", "controversia sobre la existencia o gravedad de la causa invocada"]'
),
(
    'pago de utilidades',
    'El trabajador reclama el pago de la participación de los trabajadores en las utilidades (PTU) no cubierta o cubierta de forma incompleta.',
    '["referencia a declaración anual o utilidad repartible", "periodo o ejercicio fiscal reclamado", "ausencia de disputa sobre la existencia de la relación laboral"]'
),
(
    'pago de horas extra',
    'El trabajador reclama el pago de tiempo extraordinario laborado y no retribuido conforme al límite legal de jornada.',
    '["jornada y horario alegados", "número de horas extra reclamadas por semana o periodo", "controversia sobre registros de asistencia o control de horario"]'
)
ON CONFLICT (name) DO NOTHING;
