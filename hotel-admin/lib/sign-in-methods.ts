type Switch = { enabled?: boolean } | undefined;

/** Whether any method on which the client types a secret (room and name, password, code) is switched on. */
export function typedSignInOn(cfg: {
  voucher?: Switch; guest_account?: Switch; pms?: Switch; email_account?: Switch; sms?: Switch; whatsapp?: Switch;
}): boolean {
  return !!(cfg.voucher?.enabled || cfg.guest_account?.enabled || cfg.pms?.enabled || cfg.email_account?.enabled ||
    cfg.sms?.enabled || cfg.whatsapp?.enabled);
}
