INSERT INTO platform_settings (key, value)
VALUES ('environment', 'development')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();
