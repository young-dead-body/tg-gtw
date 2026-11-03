DROP TRIGGER telegram_reset_links ON telegram_bindings;
DROP FUNCTION revoke_reset_on_telegram_link();
DROP TRIGGER users_password_history ON users;
DROP FUNCTION record_password_change();
