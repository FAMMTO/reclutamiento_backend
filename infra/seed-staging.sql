-- seed-staging.sql — datos de prueba realistas para UAT en staging.
--
-- PRE-REQUISITOS:
--   1. Iniciar el API una vez con SEED_ADMIN_EMAIL/SEED_ADMIN_PASSWORD para crear al admin.
--   2. Loguarse como admin en el dashboard y crear 1-2 reclutadores ejecutivos
--      (la contraseña temporal la asigna el admin; argon2id no se puede generar en SQL).
--   3. Ejecutar este script: psql "$STAGING_DATABASE_URL" -f seed-staging.sql
--
-- Lo que crea: 3 compañías, 4 vacantes (2 publicadas/1 borrador/1 cerrada),
--              3 candidatos, 3 postulaciones, 1 ruta de transporte.

DO $$
DECLARE
  v_org      uuid;
  v_admin    uuid;
  v_cmp1     uuid;
  v_cmp2     uuid;
  v_cmp3     uuid;
  v_vac1     uuid;
  v_vac2     uuid;
  v_vac3     uuid;
  v_vac4     uuid;
  v_cand1    uuid;
  v_cand2    uuid;
  v_cand3    uuid;
  v_ruta1    uuid;
BEGIN

  -- ── Organización y admin ya creados por SEED_ADMIN ───────────────────────
  SELECT id INTO v_org   FROM organizations   LIMIT 1;
  SELECT id INTO v_admin FROM recruiters WHERE organization_id = v_org LIMIT 1;

  IF v_org IS NULL THEN
    RAISE EXCEPTION 'No hay organización. Inicia el API primero para que SEED_ADMIN cree al admin.';
  END IF;

  -- ── Compañías ─────────────────────────────────────────────────────────────
  INSERT INTO companies (organization_id, name, created_by) VALUES
    (v_org, 'Grupo Industrial del Norte',    v_admin)  RETURNING id INTO v_cmp1;
  INSERT INTO companies (organization_id, name, created_by) VALUES
    (v_org, 'Planta Monterrey SA de CV',     v_admin)  RETURNING id INTO v_cmp2;
  INSERT INTO companies (organization_id, name, created_by) VALUES
    (v_org, 'Logística Express de México',   v_admin)  RETURNING id INTO v_cmp3;

  -- ── Vacantes ──────────────────────────────────────────────────────────────
  -- 1. Publicada — candidatos pueden postularse
  INSERT INTO vacancies
    (organization_id, company_id, survey_name, job_title, job_description,
     state, municipality, work_mode, salary_range, schedule,
     requested_sex, education_levels, activities, custom_boxes,
     status, published_at, created_by)
  VALUES (
    v_org, v_cmp1,
    'Evaluación Coordinador de Operaciones 2026',
    'Coordinador de Operaciones',
    'Responsable de coordinar la operación diaria del área de producción, '
    'dar seguimiento a KPIs y reportar avances a gerencia.',
    'Nuevo León', 'Monterrey', 'Presencial',
    '$28,000 – $35,000 MXN mensual',
    'Lunes a viernes 8:00 – 17:00',
    'Ambos',
    ARRAY['Preparatoria', 'Universidad'],
    '["Coordinar equipo de 10 personas","Reportar KPIs diarios","Gestionar proveedores"]',
    '["¿Por qué te interesa este puesto?","Describe tu mayor logro profesional"]',
    'published', now() - interval '3 days', v_admin
  ) RETURNING id INTO v_vac1;

  -- 2. Publicada — otra vacante activa
  INSERT INTO vacancies
    (organization_id, company_id, survey_name, job_title, job_description,
     state, municipality, work_mode, salary_range, schedule,
     requested_sex, education_levels, activities, custom_boxes,
     status, published_at, created_by)
  VALUES (
    v_org, v_cmp2,
    'Evaluación Operador de Producción',
    'Operador de Producción',
    'Operar maquinaria de producción conforme a estándares de calidad y seguridad.',
    'Nuevo León', 'San Nicolás de los Garza', 'Presencial',
    '$18,000 – $22,000 MXN mensual',
    'Lunes a sábado, turnos rotativos',
    'Hombre',
    ARRAY['Secundaria', 'Preparatoria'],
    '["Operar maquinaria CNC","Llevar bitácora de producción","Mantener área limpia"]',
    '["¿Tienes experiencia con maquinaria industrial?"]',
    'published', now() - interval '1 day', v_admin
  ) RETURNING id INTO v_vac2;

  -- 3. Borrador — en preparación
  INSERT INTO vacancies
    (organization_id, company_id, survey_name, job_title, job_description,
     state, municipality, work_mode, salary_range, schedule,
     requested_sex, activities, status, created_by)
  VALUES (
    v_org, v_cmp3,
    'Evaluación Analista Logística',
    'Analista de Logística',
    'Analizar rutas de distribución y optimizar tiempos de entrega.',
    'Nuevo León', 'Monterrey', 'Hibrida',
    '$22,000 – $27,000 MXN mensual',
    'Lunes a viernes 9:00 – 18:00',
    'Ambos',
    '["Analizar rutas","Negociar con transportistas","Generar reportes"]',
    'draft', v_admin
  ) RETURNING id INTO v_vac3;

  -- 4. Cerrada — para probar filtros
  INSERT INTO vacancies
    (organization_id, company_id, survey_name, job_title, job_description,
     state, municipality, work_mode, requested_sex, activities,
     status, published_at, created_by)
  VALUES (
    v_org, v_cmp1,
    'Evaluación Auxiliar Administrativo (cerrada)',
    'Auxiliar Administrativo',
    'Apoyo en tareas administrativas generales.',
    'Nuevo León', 'Monterrey', 'Presencial',
    'Ambos',
    '["Archivar documentos","Atender llamadas","Apoyar en reportes"]',
    'closed', now() - interval '30 days', v_admin
  ) RETURNING id INTO v_vac4;

  -- ── Candidatos y postulaciones ────────────────────────────────────────────
  INSERT INTO candidates
    (organization_id, phone, name, age, email, state, municipality,
     education, degree, desired_salary)
  VALUES
    (v_org, '+52 81 2345 6789', 'María González Pérez', 29,
     'maria.gonzalez@example.com', 'Nuevo León', 'Monterrey',
     'Universidad', 'Ing. Industrial', '$30,000 MXN')
  RETURNING id INTO v_cand1;

  INSERT INTO candidates
    (organization_id, phone, name, age, email, state, municipality,
     education, degree, desired_salary)
  VALUES
    (v_org, '+52 81 3456 7890', 'Carlos Ramírez López', 24,
     'carlos.ramirez@example.com', 'Nuevo León', 'San Nicolás de los Garza',
     'Preparatoria', '', '$20,000 MXN')
  RETURNING id INTO v_cand2;

  INSERT INTO candidates
    (organization_id, phone, name, age, email, state, municipality,
     education, desired_salary)
  VALUES
    (v_org, '+52 81 4567 8901', 'Ana Torres Mendoza', 31,
     'ana.torres@example.com', 'Nuevo León', 'Guadalupe',
     'Universidad', '$32,000 MXN')
  RETURNING id INTO v_cand3;

  -- Postulaciones a la vacante 1 (Coordinador)
  INSERT INTO applications
    (organization_id, candidate_id, vacancy_id, answers, status)
  VALUES
    (v_org, v_cand1, v_vac1,
     '[{"question":"¿Por qué te interesa este puesto?","answer":"Tengo 5 años coordinando equipos y busco un nuevo reto profesional."},{"question":"Describe tu mayor logro profesional","answer":"Reduje costos de producción un 15% optimizando procesos."}]',
     'entrevista'),
    (v_org, v_cand3, v_vac1,
     '[{"question":"¿Por qué te interesa este puesto?","answer":"Me apasiona la gestión de operaciones y creo que puedo aportar mucho valor."}]',
     'nueva');

  -- Postulación a la vacante 2 (Operador)
  INSERT INTO applications
    (organization_id, candidate_id, vacancy_id, answers, status)
  VALUES
    (v_org, v_cand2, v_vac2,
     '[{"question":"¿Tienes experiencia con maquinaria industrial?","answer":"Sí, 2 años operando tornos CNC en Planta Norte."}]',
     'en_revision');

  -- ── Ruta de transporte ────────────────────────────────────────────────────
  INSERT INTO rutas
    (organization_id, ubicacion, horario, lat, lng, created_by)
  VALUES
    (v_org,
     'Ruta Norte — Colonia Independencia a Planta Industrial km 12',
     'Salida 7:00 am · Regreso 6:30 pm · Lunes a sábado',
     25.6866, -100.3161,
     v_admin)
  RETURNING id INTO v_ruta1;

  -- Vincular ruta a las dos vacantes publicadas
  INSERT INTO ruta_vacancies (ruta_id, vacancy_id) VALUES
    (v_ruta1, v_vac1),
    (v_ruta1, v_vac2);

  RAISE NOTICE 'Seed completado. Organización: %, vacantes: 4 (2 publicadas, 1 borrador, 1 cerrada), candidatos: 3, postulaciones: 3.', v_org;

END $$;
