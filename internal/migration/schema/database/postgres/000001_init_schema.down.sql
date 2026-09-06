BEGIN;

DROP TABLE auth_assertion_receipts;

DROP TABLE quota_batch_deliveries;

DROP TABLE quota_batch_receipts;

DROP TABLE vendors;
DROP TABLE users;
DROP TABLE user_subscriptions;
DROP TABLE user_oauth_bindings;
DROP TABLE two_fas;
DROP TABLE two_fa_backup_codes;
DROP TABLE top_ups;
DROP TABLE tokens;
DROP TABLE tasks;
DROP TABLE task_plugins;
DROP TABLE system_tasks;
DROP TABLE subscription_pre_consume_records;
DROP TABLE subscription_plans;
DROP TABLE subscription_orders;
DROP TABLE setups;
DROP TABLE redemptions;
DROP TABLE quota_data;
DROP TABLE prefill_groups;
DROP TABLE perf_metrics;
DROP TABLE passkey_credentials;
DROP TABLE options;
DROP TABLE models;
DROP TABLE midjourneys;
DROP TABLE login_encryption_keys;
DROP TABLE external_identity_claims;
DROP TABLE custom_oauth_providers;
DROP TABLE checkins;
DROP TABLE channels;
DROP TABLE casbin_rule;
DROP TABLE authz_roles;
DROP TABLE abilities;

COMMIT;
