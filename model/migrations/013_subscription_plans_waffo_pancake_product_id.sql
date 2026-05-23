-- Add Waffo Pancake product binding for subscription plans.

ALTER TABLE subscription_plans
  ADD COLUMN IF NOT EXISTS waffo_pancake_product_id varchar(128) DEFAULT '';

UPDATE subscription_plans
SET waffo_pancake_product_id = ''
WHERE waffo_pancake_product_id IS NULL;
