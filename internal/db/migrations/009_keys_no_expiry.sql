-- keys don't expire: they work while the person is in a main chat and until /revoke
update api_keys set expires_at = null where revoked_at is null;
