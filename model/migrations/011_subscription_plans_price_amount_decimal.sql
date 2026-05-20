-- Migrate subscription_plans.price_amount to decimal(10,6).

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_schema = current_schema()
      AND table_name = 'subscription_plans'
      AND column_name = 'price_amount'
      AND (
        data_type <> 'numeric'
        OR numeric_precision IS DISTINCT FROM 10
        OR numeric_scale IS DISTINCT FROM 6
      )
  ) THEN
    ALTER TABLE subscription_plans
      ALTER COLUMN price_amount TYPE decimal(10,6)
      USING price_amount::decimal(10,6);
  END IF;
END $$;
