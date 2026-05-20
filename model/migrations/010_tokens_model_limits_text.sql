-- Migrate tokens.model_limits from varchar to text.

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_schema = current_schema()
      AND table_name = 'tokens'
      AND column_name = 'model_limits'
      AND data_type <> 'text'
  ) THEN
    ALTER TABLE tokens ALTER COLUMN model_limits TYPE text;
  END IF;
END $$;
