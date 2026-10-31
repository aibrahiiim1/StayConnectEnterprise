-- LAB INSTRUMENT ONLY (not part of the product): records the instant a session becomes active (enforced), to measure the ENFORCED rate.
create schema if not exists lab;
create table if not exists lab.session_activation (at timestamptz not null default clock_timestamp(), session_id uuid, ip inet);
create or replace function lab.log_activation() returns trigger language plpgsql security definer as $$
begin
  if NEW.state = 'active' and OLD.state is distinct from 'active' then
    insert into lab.session_activation(session_id, ip) values (NEW.id, NEW.ip);
  end if;
  return NEW;
end $$;
drop trigger if exists lab_log_activation on iam_v2.sessions;
create trigger lab_log_activation after update of state on iam_v2.sessions for each row execute function lab.log_activation();
