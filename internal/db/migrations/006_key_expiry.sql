-- keys expire; several active keys per person are allowed (one per device/app)
alter table api_keys add column expires_at timestamptz;
update api_keys set expires_at = now() + interval '180 days' where revoked_at is null;
