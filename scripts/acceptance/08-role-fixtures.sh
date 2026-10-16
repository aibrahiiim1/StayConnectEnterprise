#!/bin/bash
# 8 (appliance side). create: one fixture operator per role (strong password, require_change=false), written to
# /root/acceptance/roles.state (0600) for the workstation sweep. disable: disables every acc-role-* operator.
. "$(dirname "$0")/lib.sh"
ST=/root/acceptance/roles.state
if [ "${1:-}" = create ]; then trap - EXIT
  PW="Role-$(head -c9 /dev/urandom | od -An -tx1 | tr -d ' \n')-Kx7"; umask 077; echo "PW=$PW" > $ST
  for role in hotel_it_manager front_office_operator guest_relations_operator voucher_operator payments_operator site_viewer; do
    e="acc-role-$role-$R4@onegate.invalid"
    id=$(admin -d "{\"email\":\"$e\",\"display_name\":\"Acceptance $role\",\"password\":\"$PW\",\"role\":\"$role\",\"require_change\":false}" $B/operators | jget id)
    mc=$($PSQL "SELECT must_change_password FROM operators WHERE id='$id'" 2>/dev/null)
    echo "$role $e" >> $ST; echo "$role -> ${id:0:8} must_change=$mc"; done; rm -f $CK
else
  for o in $($PSQL "SELECT id FROM operators WHERE email LIKE 'acc-role-%@onegate.invalid' AND status <> 'disabled'"); do OPS+=("$o"); done
  info "disabling ${#OPS[@]} role fixtures"; rm -f $ST
fi
