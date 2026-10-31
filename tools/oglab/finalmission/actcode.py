import base64,hashlib,sys,json
pk=json.load(open("/etc/stayconnect/identity/identity.json"))["public_key"]
raw=base64.urlsafe_b64decode(pk+"="*(-len(pk)%4))
h=hashlib.sha256(b"onegate-activation-code-v1\x00"+raw).digest()
e=base64.b32encode(h).decode().rstrip("=")
print(e[:4]+"-"+e[4:8])
