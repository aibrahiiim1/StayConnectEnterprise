BEGIN;

DROP TABLE IF EXISTS central_notification_outbox;
DROP TABLE IF EXISTS central_notification_episodes;
DROP TABLE IF EXISTS central_notification_subscriptions;
DROP TABLE IF EXISTS central_notification_settings;

COMMIT;
