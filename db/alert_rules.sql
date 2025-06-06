-- Run this in your Supabase SQL editor
CREATE TABLE alert_rules (
  id SERIAL PRIMARY KEY,
  topics TEXT[] NOT NULL,
  table TEXT NOT NULL,
  field TEXT NOT NULL,
  conditions JSONB NOT NULL,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW()
);


-- Enable Row Level Security if needed
ALTER TABLE alert_rules ENABLE ROW LEVEL SECURITY;

-- Exposing custom schemas
-- Grant Access for Service role to by pass RLS policy
GRANT USAGE ON SCHEMA dashboard_logs TO anon,
authenticated,
service_role;

GRANT ALL ON ALL TABLES IN SCHEMA dashboard_logs TO anon,
authenticated,
service_role;

GRANT ALL ON ALL ROUTINES IN SCHEMA dashboard_logs TO anon,
authenticated,
service_role;

GRANT ALL ON ALL SEQUENCES IN SCHEMA dashboard_logs TO anon,
authenticated,
service_role;

ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA dashboard_logs GRANT ALL ON TABLES TO anon,
authenticated,
service_role;

ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA dashboard_logs GRANT ALL ON ROUTINES TO anon,
authenticated,
service_role;

ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA dashboard_logs GRANT ALL ON SEQUENCES TO anon,
authenticated,
service_role
-- Test Rule
INSERT INTO
  "dashboard_logs"."alert_rules" (
    "id",
    "topics",
    "table",
    "field",
    "conditions",
    "created_at",
    "updated_at"
  )
VALUES
  (
    '1',
    '{"nk3/holding_register/all/D800","nk3/holding_register/all/D392","nk3/holding_register/all/D166"}',
    'logs_temp',
    'value',
    '[{"level": 3, "device": "D800", "operator": "D800 < 900 AND D392 == D166 AND D166 != 0", "threshold": 900, "message_template": "{{address}} current: {{value}} is below threshold: {{threshold}}"}]',
    '2025-05-16 08:43:25.490468+08',
    '2025-05-16 08:43:25.490468+08'
  );

INSERT INTO
  "dashboard_logs"."alert_rules" (
    "id",
    "topics",
    "table",
    "field",
    "conditions",
    "created_at",
    "updated_at"
  )
VALUES
  (
    '2',
    '{"nk3/holding_register/all/D800","nk3/holding_register/all/D392","nk3/holding_register/all/D166"}',
    'logs_temp',
    'value',
    '[{"level": 3, "device": "D800", "operator": "D800 < 900 AND D392 == D166 AND D166 != 0", "threshold": 900, "message_template": "{{address}} current: {{value}} is below threshold: {{threshold}}"}]',
    '2025-05-16 08:43:25.490468+08',
    '2025-05-16 08:43:25.490468+08'
  );