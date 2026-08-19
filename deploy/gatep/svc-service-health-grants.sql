-- appliance_service_health: the self-reported health row every data-plane
-- service upserts.
--
-- Found in the PostgreSQL log while diagnosing an unrelated failure: three of
-- the four services could not write it at all (svc_scd, svc_acctd and svc_netd
-- had neither INSERT nor UPDATE) and svc_edged had INSERT but not the UPDATE its
-- ON CONFLICT DO UPDATE needs. So the health table -- the thing an operator
-- would look at to decide whether the appliance is healthy -- was being written
-- by nobody, silently, while every service reported itself active.
--
-- INSERT and UPDATE only, no DELETE: a service reports its own state and never
-- removes another's row.
GRANT INSERT, UPDATE, SELECT ON public.appliance_service_health TO svc_scd;
GRANT INSERT, UPDATE, SELECT ON public.appliance_service_health TO svc_edged;
GRANT INSERT, UPDATE, SELECT ON public.appliance_service_health TO svc_acctd;
GRANT INSERT, UPDATE, SELECT ON public.appliance_service_health TO svc_netd;
