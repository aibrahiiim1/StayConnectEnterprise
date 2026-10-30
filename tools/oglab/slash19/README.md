# /19 client network lab helpers

TEST tools for [`docs/runbooks/NETWORK_19_LAB_PLAN.md`](../../../docs/runbooks/NETWORK_19_LAB_PLAN.md). Nothing here is installed on an appliance.

| File | Use |
|---|---|
| `Dockerfile.shape` | `c19-lab:1`: the Go toolchain image plus `iproute2`, `python3`, `ping`; the privileged real-kernel tests (`SHAPE_REAL=1`) run in it |
| `Dockerfile.kea` | `c19-kea:1`: a real `kea-dhcp4` (Debian 12 packages are Kea 2.2; the appliance runs the Ubuntu 22.04 package, Kea 2.0.x: rerun on the appliance's own Kea in the lab) |
| `dhcpclient.py` | a small DHCPv4 client: DORA for N distinct MACs on one interface, or INIT-REBOOT renewal of the leases it recorded |
| `kea_slash19_lab.sh` | runs inside `c19-kea:1`: builds a `/19` bridge, starts Kea on the config the product renders (`KEA_FIXTURE_OUT`, see below), leases the whole pool, restarts Kea, renews |

The Kea file is the product's own render of a `/19` network: `KEA_FIXTURE_OUT=/path/kea-fixture.json go test ./internal/netcfg -run TestWriteKeaSlash19Fixture`
(from `data-plane/`). Real-kernel shaping: `SHAPE_REAL=1 SHAPE_REAL_N=6000 go test ./internal/shape -run 'TestRealKernelSlash19' -v` in a **privileged**
`c19-lab:1` container (use a unique container name; the tests create and delete their own bridges `br-s19a`, `br-s19b`, `br-s19n` and namespaces).
