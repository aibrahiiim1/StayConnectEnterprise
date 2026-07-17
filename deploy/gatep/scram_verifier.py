#!/usr/bin/env python3
"""Compute a PostgreSQL SCRAM-SHA-256 verifier from a password read on STDIN.

The cleartext password is read from stdin (never argv), and only the SCRAM verifier
(a non-reversible hash structure) is printed. `ALTER ROLE ... PASSWORD '<verifier>'` therefore
never contains cleartext, so no cleartext can appear in SQL, argv, process listings or logs.
Runs on the appliance (uses os.urandom for the salt). stdlib only.
"""
import sys, os, hashlib, hmac, base64

def scram_sha256(password: str, iterations: int = 4096) -> str:
    salt = os.urandom(16)
    salted = hashlib.pbkdf2_hmac("sha256", password.encode("utf-8"), salt, iterations)
    client_key = hmac.new(salted, b"Client Key", hashlib.sha256).digest()
    stored_key = hashlib.sha256(client_key).digest()
    server_key = hmac.new(salted, b"Server Key", hashlib.sha256).digest()
    return (f"SCRAM-SHA-256${iterations}:{base64.b64encode(salt).decode()}"
            f"${base64.b64encode(stored_key).decode()}:{base64.b64encode(server_key).decode()}")

if __name__ == "__main__":
    pw = sys.stdin.buffer.read().decode("utf-8").rstrip("\n")
    if not pw:
        sys.exit("empty password on stdin")
    sys.stdout.write(scram_sha256(pw) + "\n")
