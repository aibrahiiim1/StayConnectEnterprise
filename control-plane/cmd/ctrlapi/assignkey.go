package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
)

// runGenRegistryKey generates the registry ROOT-OF-TRUST key. Its private half
// signs the versioned trust registry on Central; its public half is the trust
// anchor baked into each appliance (assignment-registry-root.pub). It is a
// separate key from the assignment-signing keys it authorises.
//
//	ctrlapi gen-registry-key --out /etc/stayconnect/assignment-registry-root.key \
//	                         --pub-out /etc/stayconnect/assignment-registry-root.pub
func runGenRegistryKey(args []string) error {
	fs := flag.NewFlagSet("gen-registry-key", flag.ExitOnError)
	out := fs.String("out", "/etc/stayconnect/assignment-registry-root.key", "private key path")
	pubOut := fs.String("pub-out", "/etc/stayconnect/assignment-registry-root.pub", "public key path (raw 32 bytes)")
	force := fs.Bool("force", false, "overwrite an existing key (rotating the root of trust re-anchors the whole fleet)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := os.Stat(*out); err == nil && !*force {
		return fmt.Errorf("%s already exists; refusing to overwrite (use --force for a deliberate root rotation)", *out)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, priv, 0o600); err != nil {
		return err
	}
	if *pubOut != "" {
		if err := os.WriteFile(*pubOut, pub, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("registry root key generated\n  key_id: %s\n  private: %s\n  public (trust anchor): %s\n",
		assignment.KeyID(pub), *out, *pubOut)
	return nil
}

// runGenAssignmentKey generates the DEDICATED Ed25519 assignment-signing key and,
// optionally, the appliance trust-registry file to ship with it.
//
// This key signs ONLY appliance-assignment documents. It must never be the license,
// command, update, CA or auth-callout key: appliances trust assignment signatures
// exclusively through the assignment trust registry, so reusing another key would
// simply be rejected as an unknown signer.
//
//	ctrlapi gen-assignment-key --out /etc/stayconnect/assignment-signing.key \
//	                           --pub-out /etc/stayconnect/assignment-signing.pub \
//	                           --registry-out /etc/stayconnect/assignment-trust.json
func runGenAssignmentKey(args []string) error {
	fs := flag.NewFlagSet("gen-assignment-key", flag.ExitOnError)
	out := fs.String("out", "/etc/stayconnect/assignment-signing.key", "private key path")
	pubOut := fs.String("pub-out", "", "public key path (raw 32 bytes)")
	regOut := fs.String("registry-out", "", "appliance trust registry JSON to write/merge")
	force := fs.Bool("force", false, "overwrite an existing private key (ROTATION — read the policy first)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if _, err := os.Stat(*out); err == nil && !*force {
		return fmt.Errorf("%s already exists; refusing to overwrite (use --force only for a deliberate rotation)", *out)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, priv, 0o600); err != nil {
		return err
	}
	if *pubOut != "" {
		if err := os.WriteFile(*pubOut, pub, 0o644); err != nil {
			return err
		}
	}
	keyID := assignment.KeyID(pub)

	if *regOut != "" {
		reg := &assignment.Registry{}
		if existing, err := assignment.LoadRegistry(*regOut); err == nil {
			reg = existing // merge: rotation keeps the old key active until retired
		}
		reg.AddOrRotate(assignment.TrustedKey{
			KeyID:     keyID,
			PublicKey: base64.StdEncoding.EncodeToString(pub),
			AddedAt:   time.Now().UTC().Format(time.RFC3339),
			Note:      "assignment signing key",
		})
		if err := reg.Save(*regOut); err != nil {
			return err
		}
		b, _ := json.Marshal(reg)
		fmt.Printf("registry: %s\n", string(b))
	}
	fmt.Printf("assignment signing key generated\n  key_id: %s\n  private: %s\n", keyID, *out)
	fmt.Println("ROTATION: distribute the updated trust registry to appliances BEFORE switching")
	fmt.Println("signing to this key; retire the previous key only after every appliance has it.")
	return nil
}
