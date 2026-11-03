-- Keep rotation time and reuse history in the same transaction as every
-- password update, including administrator resets and one-time links.
CREATE FUNCTION record_password_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.password_hash IS DISTINCT FROM OLD.password_hash THEN
        NEW.password_changed_at := now();
        DELETE FROM one_time_tokens WHERE user_id = NEW.id AND purpose = 'password_reset';
        IF NEW.password_hash IS NOT NULL THEN
            INSERT INTO password_history (user_id, password_hash) VALUES (NEW.id, NEW.password_hash);
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER users_password_history BEFORE UPDATE OF password_hash ON users
FOR EACH ROW EXECUTE FUNCTION record_password_change();

CREATE FUNCTION revoke_reset_on_telegram_link() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM one_time_tokens WHERE user_id = NEW.user_id AND purpose = 'password_reset';
    RETURN NEW;
END;
$$;
CREATE TRIGGER telegram_reset_links AFTER INSERT OR UPDATE ON telegram_bindings
FOR EACH ROW EXECUTE FUNCTION revoke_reset_on_telegram_link();
