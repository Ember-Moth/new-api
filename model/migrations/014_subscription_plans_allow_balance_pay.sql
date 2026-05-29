-- Add per-plan balance redemption visibility/control.

ALTER TABLE subscription_plans
  ADD COLUMN IF NOT EXISTS allow_balance_pay boolean DEFAULT true;

UPDATE subscription_plans
SET allow_balance_pay = true
WHERE allow_balance_pay IS NULL;
