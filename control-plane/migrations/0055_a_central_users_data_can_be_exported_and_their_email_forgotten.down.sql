-- 0055 down. Emails already replaced stay replaced: nothing can bring them back.
BEGIN;
DROP FUNCTION IF EXISTS central_user_data(uuid);
DROP TRIGGER IF EXISTS operators_forget_on_anonymise ON operators;
DROP FUNCTION IF EXISTS operators_forget_on_anonymise();
DROP TRIGGER IF EXISTS operators_forget_on_delete ON operators;
DROP FUNCTION IF EXISTS operators_forget_on_delete();
DROP FUNCTION IF EXISTS central_forget_actor(text, text);
COMMIT;
