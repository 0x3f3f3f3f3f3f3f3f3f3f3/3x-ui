package policyauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

type mappingJournalAPI interface {
	RecordClientMapping(ClientMappingSide, ClientMapping) error
	LookupClientMapping(ClientMappingSide, string, string) (ClientMapping, error)
	ClientMappings(ClientMappingSide, string, int) ([]ClientMapping, error)
}

func mappingAPI(t *testing.T, j *Journal) mappingJournalAPI {
	t.Helper()
	api, ok := any(j).(mappingJournalAPI)
	if !ok {
		t.Fatal("original journal cannot retain canonical/node mapping evidence")
	}
	return api
}

func mappingFixture(t *testing.T, side ClientMappingSide, change func(*Seed)) (*Journal, ClientMapping, string) {
	t.Helper()
	m := ClientMapping{Authority: Identity{"mapping-authority", 1}, NodeAnchor: Identity{"node-anchor-a", 1}, NodeID: "node-a", SourceID: "mapping-node-source", GlobalClientID: "11111111-1111-4111-8111-111111111111", LocalClientID: "22222222-2222-4222-8222-222222222222", GlobalPolicyVersion: 7, LocalPolicyVersion: 1, PolicyDigest: strings.Repeat("a", 64)}
	id, source, client, version := m.Authority, "coordinator-source", m.GlobalClientID, m.GlobalPolicyVersion
	role := []byte(`{"mode":"local"}`)
	if side == ClientMappingNode {
		id, source, client, version = m.NodeAnchor, m.SourceID, m.LocalClientID, m.LocalPolicyVersion
		role = []byte(`{"mode":"delegated","authorityId":"mapping-authority","generation":1,"nodeId":"node-a"}`)
	}
	seed := Seed{ClientID: client, Policy: Policy{WindowID: "initial:" + client, Version: version, QuotaBytes: 100, Upload: Direction{Unlimited: true}, Download: Direction{Unlimited: true}}}
	if change != nil {
		change(&seed)
	}
	dir := filepath.Join(t.TempDir(), "private-mapping")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journal.db")
	j, err := CreateWithMigrationSourceIdentity(path, id, source, []Seed{seed}, nil, []MigrationRecord{{Kind: "execution-role", Key: source, Value: role}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j, m, path
}

func TestClientMappingRetainsBothAnchorsAndUsedRetry(t *testing.T) {
	for _, side := range []ClientMappingSide{ClientMappingNode, ClientMappingCoordinator} {
		t.Run(string(side), func(t *testing.T) {
			j, m, path := mappingFixture(t, side, nil)
			api := mappingAPI(t, j)
			if err := api.RecordClientMapping(side, m); err != nil {
				t.Fatal(err)
			}
			if side == ClientMappingCoordinator {
				boot := NodeBoot{m.NodeID, m.SourceID, "boot-a"}
				if err := j.RegisterBoot(boot); err != nil {
					t.Fatal(err)
				}
				account, _ := j.Account(m.GlobalClientID)
				grant, err := j.Issue(Request{Binding: Binding{j.Identity(), boot, m.GlobalClientID, account.Policy.WindowID, m.GlobalPolicyVersion}, RequestID: "actual-allocation", ChallengeID: "challenge", Capacity: 40, Upload: account.Policy.Upload, Download: account.Policy.Download, LeaseDuration: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				if err := j.Report(Report{Binding: grant.Request.Binding, GrantID: grant.GrantID, Sequence: 1, Usage: Usage{RawUpload: 8, RawDownload: 2, BilledBytes: 20, Remainder: 123}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := api.RecordClientMapping(side, m); err != nil {
				t.Fatalf("exact retry failed after use: %v", err)
			}
			got, err := api.LookupClientMapping(side, m.SourceID, m.LocalClientID)
			if err != nil || got != m {
				t.Fatalf("mapping changed: %+v/%v", got, err)
			}
			got.GlobalClientID = "caller-only"
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(path, j.Identity())
			if err != nil {
				t.Fatalf("mapped journal did not reopen: %v", err)
			}
			defer reopened.Close()
			page, err := mappingAPI(t, reopened).ClientMappings(side, "", 128)
			if err != nil || len(page) != 1 || page[0] != m {
				t.Fatalf("original mapping did not survive reopen: %+v/%v", page, err)
			}
			if side == ClientMappingCoordinator {
				account, err := reopened.Account(m.GlobalClientID)
				if err != nil || account.Usage.BilledBytes != 20 || account.Usage.Remainder != 123 || account.HeldCapacity != 19 || account.HeldRemainder != 999877 {
					t.Fatalf("mapping reset accounting: %+v/%v", account, err)
				}
			}
		})
	}
}

func TestClientMappingRefusesRetargetAndHiddenHistory(t *testing.T) {
	for _, side := range []ClientMappingSide{ClientMappingNode, ClientMappingCoordinator} {
		t.Run(string(side), func(t *testing.T) {
			j, m, _ := mappingFixture(t, side, nil)
			api := mappingAPI(t, j)
			if err := api.RecordClientMapping(side, m); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"global", "local", "source", "node", "authority", "anchor", "global-version", "local-version", "digest"} {
				t.Run(field, func(t *testing.T) {
					bad := m
					switch field {
					case "global":
						bad.GlobalClientID = "33333333-3333-4333-8333-333333333333"
					case "local":
						bad.LocalClientID = "33333333-3333-4333-8333-333333333333"
					case "source":
						bad.SourceID = "other-source"
					case "node":
						bad.NodeID = "other-node"
					case "authority":
						bad.Authority.Generation++
					case "anchor":
						bad.NodeAnchor.Generation++
					case "global-version":
						bad.GlobalPolicyVersion++
					case "local-version":
						bad.LocalPolicyVersion++
					case "digest":
						bad.PolicyDigest = strings.Repeat("b", 64)
					}
					if err := api.RecordClientMapping(side, bad); err == nil {
						t.Fatal("existing enrollment was retargeted")
					}
				})
			}
			for _, limit := range []int{0, 129} {
				if _, err := api.ClientMappings(side, "", limit); err == nil {
					t.Fatal("unbounded mapping page accepted")
				}
			}
			if _, err := api.LookupClientMapping(side, m.SourceID, "missing"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("missing mapping synthesized: %v", err)
			}
		})
	}
	for _, hidden := range []string{"raw", "fraction", "window", "window-fraction", "frozen"} {
		t.Run(hidden, func(t *testing.T) {
			j, m, _ := mappingFixture(t, ClientMappingNode, func(seed *Seed) {
				switch hidden {
				case "raw":
					seed.Usage.RawUpload = 1
				case "fraction":
					seed.Usage.Remainder = 1
				case "window":
					seed.WindowUsed = 1
				case "window-fraction":
					seed.WindowRemainder = 1
				case "frozen":
					seed.FrozenBilled = 1
				}
			})
			if err := mappingAPI(t, j).RecordClientMapping(ClientMappingNode, m); err == nil {
				t.Fatal("consumed or uncertain account enrolled as fresh")
			}
		})
	}
}

func TestClientMappingFencesOlderWritersAndCorruption(t *testing.T) {
	j, m, path := mappingFixture(t, ClientMappingNode, nil)
	if err := mappingAPI(t, j).RecordClientMapping(ClientMappingNode, m); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"RESET_OPERATION_OLD_WRITER_PROBE", "RESET_OPERATION_SCHEMA5_WRITER_PROBE", "CLIENT_MAPPING_SCHEMA6_WRITER_PROBE"} {
		probe := mappingOldWriterProbe(t, name)
		result, err := exec.Command(probe, path, j.Identity().AuthorityID, strconv.FormatUint(j.Identity().Generation, 10)).CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 3 || !strings.Contains(string(result), "REJECTED_JOURNAL") {
			t.Fatalf("older writer accepted mapped evidence: %s/%v", result, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("older writer changed mapped journal bytes")
		}
	}
	// A valid bbolt file with its identity fence downgraded must still refuse.
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		meta.Schema = 4
		return put(tx, "metadata", "state", meta)
	})
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(path, j.Identity()); err == nil {
		_ = reopened.Close()
		t.Fatal("mapping bucket survived an unprotected schema downgrade")
	}
}

func mappingOldWriterProbe(t *testing.T, name string) string {
	t.Helper()
	if probe := os.Getenv(name); probe != "" {
		return probe
	}
	schema := map[string]int{"RESET_OPERATION_OLD_WRITER_PROBE": 4, "RESET_OPERATION_SCHEMA5_WRITER_PROBE": 5, "CLIENT_MAPPING_SCHEMA6_WRITER_PROBE": 6, "MANAGED_ACCOUNT_SCHEMA7_WRITER_PROBE": 7}[name]
	fixture := filepath.Join("..", "..", "tools", "fixtures", "client-mapping-schema"+strconv.Itoa(schema)+"-writer")
	if schema == 7 {
		fixture = filepath.Join("..", "..", "tools", "fixtures", "managed-account-schema7-writer")
	}
	raw, err := os.ReadFile(filepath.Join(fixture, "SOURCE.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		MaximumJournalSchema int               `json:"maximumJournalSchema"`
		Files                map[string]string `json:"files"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.MaximumJournalSchema != schema || len(manifest.Files) == 0 {
		t.Fatal("invalid older-writer source manifest")
	}
	for path, expected := range manifest.Files {
		if filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasPrefix(path, "..") {
			t.Fatal("invalid source manifest path")
		}
		data, err := os.ReadFile(filepath.Join(fixture, path))
		digest := sha256.Sum256(data)
		if err != nil || hex.EncodeToString(digest[:]) != expected {
			t.Fatalf("older-writer fixture source changed: %s/%v", path, err)
		}
	}
	probe := filepath.Join(t.TempDir(), "older-writer")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", probe, "./cmd/old-writer-probe")
	cmd.Dir = fixture
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build real schema%d writer: %s/%v", schema, output, err)
	}
	t.Logf("real schema%d older writer built from frozen source", schema)
	return probe
}

func TestClientMappingPreservesResetProgressFence(t *testing.T) {
	for _, floor := range []int{4, 5, 6} {
		t.Run(strconv.Itoa(floor), func(t *testing.T) {
			j, m, path := mappingFixture(t, ClientMappingCoordinator, nil)
			capture := ResetOperationCapture{Identity: j.Identity(), SourceID: "coordinator-source", RequestID: "original", Snapshot: `{"members":["canonical"]}`}
			prepared := ResetOperationPreparation{Identity: j.Identity(), SourceID: capture.SourceID, RequestID: capture.RequestID, CaptureDigest: resetTestDigest(capture.Snapshot), Snapshot: `{"version":7}`}
			completion := ResetOperationCompletion{Identity: j.Identity(), SourceID: capture.SourceID, RequestID: capture.RequestID, PreparationDigest: resetTestDigest(prepared.Snapshot)}
			if floor >= 5 {
				if err := j.CaptureResetOperation(capture); err != nil {
					t.Fatal(err)
				}
			}
			if floor == 6 {
				if err := j.PrepareResetOperation(prepared); err != nil {
					t.Fatal(err)
				}
				if err := j.CompleteResetOperation(completion); err != nil {
					t.Fatal(err)
				}
			}
			if err := mappingAPI(t, j).RecordClientMapping(ClientMappingCoordinator, m); err != nil {
				t.Fatal(err)
			}
			if err := j.CaptureResetOperation(capture); err != nil {
				t.Fatal(err)
			}
			if err := j.PrepareResetOperation(prepared); err != nil {
				t.Fatal(err)
			}
			if err := j.CompleteResetOperation(completion); err != nil {
				t.Fatal(err)
			}
			if err := j.db.View(func(tx *bolt.Tx) error {
				var meta metadata
				if err := get(tx, "metadata", "state", &meta); err != nil {
					return err
				}
				if meta.Schema != 7 || meta.MappingBaseSchema != 6 {
					t.Fatalf("reset changed mapping fence: %+v", meta)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			_ = j.Close()
			reopened, err := Open(path, j.Identity())
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			got, err := reopened.LookupResetCompletion(capture.RequestID)
			if err != nil || got != completion {
				t.Fatalf("mapping discarded reset completion: %+v/%v", got, err)
			}
		})
	}
}

func TestClientMappingRefusesInitialSourceAndHistoricalGrant(t *testing.T) {
	for _, fault := range []string{"authority", "source", "node", "unknown-client", "digest-case", "deleted", "zero-sealed-grant"} {
		t.Run(fault, func(t *testing.T) {
			j, m, _ := mappingFixture(t, ClientMappingNode, nil)
			switch fault {
			case "authority":
				m.Authority.Generation++
			case "source":
				m.SourceID = "foreign-source"
			case "node":
				m.NodeID = "foreign-node"
			case "unknown-client":
				m.LocalClientID = "44444444-4444-4444-8444-444444444444"
			case "digest-case":
				m.PolicyDigest = strings.ToUpper(m.PolicyDigest)
			case "deleted":
				if err := j.Tombstone(m.LocalClientID); err != nil {
					t.Fatal(err)
				}
			case "zero-sealed-grant":
				boot := NodeBoot{m.NodeID, m.SourceID, "prior-boot"}
				if err := j.RegisterBoot(boot); err != nil {
					t.Fatal(err)
				}
				account, _ := j.Account(m.LocalClientID)
				grant, err := j.Issue(Request{Binding: Binding{j.Identity(), boot, m.LocalClientID, account.Policy.WindowID, m.LocalPolicyVersion}, RequestID: "zero-but-previously-issued", ChallengeID: "challenge", Capacity: 1, Upload: account.Policy.Upload, Download: account.Policy.Download, LeaseDuration: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				if err := j.Report(Report{Binding: grant.Request.Binding, GrantID: grant.GrantID, Sequence: 1, Seal: true}); err != nil {
					t.Fatal(err)
				}
			}
			if err := mappingAPI(t, j).RecordClientMapping(ClientMappingNode, m); err == nil {
				t.Fatal("unproven or previously issued local source enrolled as fresh")
			}
		})
	}
}

func TestClientMappingPagesAcrossNodesWithoutResettingGlobalBalance(t *testing.T) {
	j, first, _ := mappingFixture(t, ClientMappingCoordinator, func(seed *Seed) {
		seed.Usage = Usage{RawUpload: 3, BilledBytes: 6, Remainder: 12}
		seed.WindowUsed = 6
		seed.WindowRemainder = 12
	})
	before, _ := j.Account(first.GlobalClientID)
	api := mappingAPI(t, j)
	second := first
	second.NodeID, second.SourceID, second.NodeAnchor = "node-b", first.SourceID+"-b", Identity{"node-anchor-b", 1}
	second.LocalClientID = "55555555-5555-4555-8555-555555555555"
	for _, mapping := range []ClientMapping{first, second} {
		if err := api.RecordClientMapping(ClientMappingCoordinator, mapping); err != nil {
			t.Fatal(err)
		}
	}
	page, err := api.ClientMappings(ClientMappingCoordinator, "", 1)
	if err != nil || len(page) != 1 || page[0] != first {
		t.Fatalf("first canonical mapping page: %+v/%v", page, err)
	}
	page, err = api.ClientMappings(ClientMappingCoordinator, ClientMappingCursor(page[0]), 1)
	if err != nil || len(page) != 1 || page[0] != second {
		t.Fatalf("next canonical mapping page: %+v/%v", page, err)
	}
	page, err = api.ClientMappings(ClientMappingCoordinator, ClientMappingCursor(second), 1)
	if err != nil || len(page) != 0 {
		t.Fatalf("mapping page replayed previous node: %+v/%v", page, err)
	}
	for _, cursor := range []string{"invalid!", ClientMappingCursor(second) + "=", strings.Repeat("a", 257)} {
		if _, err := api.ClientMappings(ClientMappingCoordinator, cursor, 1); err == nil {
			t.Fatal("ambiguous or unbounded mapping cursor accepted")
		}
	}
	after, err := j.Account(first.GlobalClientID)
	if err != nil || after != before {
		t.Fatalf("adding nodes reset the canonical global balance: %+v/%v", after, err)
	}
}

func TestClientMappingCursorFitsMaximumSource(t *testing.T) {
	j, m, _ := mappingFixture(t, ClientMappingCoordinator, nil)
	m.SourceID = "s" + strings.Repeat("\x01", 127)
	if err := mappingAPI(t, j).RecordClientMapping(ClientMappingCoordinator, m); err != nil {
		t.Fatal(err)
	}
	page, err := mappingAPI(t, j).ClientMappings(ClientMappingCoordinator, ClientMappingCursor(m), 1)
	if err != nil || len(page) != 0 {
		t.Fatalf("a valid maximum source cannot use its own bounded cursor: %v", err)
	}
}

func TestClientMappingConcurrentRetryCommitsOnce(t *testing.T) {
	j, m, _ := mappingFixture(t, ClientMappingNode, nil)
	api := mappingAPI(t, j)
	results := make(chan error, 16)
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() { results <- api.RecordClientMapping(ClientMappingNode, m) })
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent exact enrollment conflicted: %v", err)
		}
	}
	page, err := api.ClientMappings(ClientMappingNode, "", 128)
	if err != nil || len(page) != 1 || page[0] != m {
		t.Fatalf("concurrent enrollment duplicated evidence: %+v/%v", page, err)
	}
	if err := j.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte(clientMappingBucket)).Sequence() != 1 {
			t.Fatal("retry advanced durable mapping count")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestClientMappingRejectsCorruption(t *testing.T) {
	j, m, path := mappingFixture(t, ClientMappingNode, nil)
	if err := mappingAPI(t, j).RecordClientMapping(ClientMappingNode, m); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"missing-reverse", "missing-node", "unknown-index", "source", "version", "sequence", "missing-bucket", "reset-floor", "unknown-field", "duplicate-field"} {
		t.Run(fault, func(t *testing.T) {
			copyPath := filepath.Join(filepath.Dir(path), fault+".db")
			if err := os.WriteFile(copyPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			db, err := bolt.Open(copyPath, 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = db.Update(func(tx *bolt.Tx) error {
				b := tx.Bucket([]byte(clientMappingBucket))
				primary := mappingRecordKey(ClientMappingNode, m.SourceID, m.LocalClientID)
				switch fault {
				case "missing-reverse":
					return b.Delete([]byte(mappingReverseKey(ClientMappingNode, m)))
				case "missing-node":
					return b.Delete([]byte(mappingNodeKey(ClientMappingNode, m)))
				case "unknown-index":
					return b.Put([]byte("unknown-index"), []byte("unproven"))
				case "sequence":
					return b.SetSequence(2)
				case "missing-bucket":
					return tx.DeleteBucket([]byte(clientMappingBucket))
				case "reset-floor":
					var meta metadata
					if err := get(tx, "metadata", "state", &meta); err != nil {
						return err
					}
					meta.MappingBaseSchema = 5
					return put(tx, "metadata", "state", meta)
				case "unknown-field":
					raw := bytes.TrimPrefix(b.Get([]byte(primary)), []byte("{"))
					return b.Put([]byte(primary), append([]byte(`{"unproven":true,`), raw...))
				case "duplicate-field":
					raw := bytes.Replace(b.Get([]byte(primary)), []byte(`"side":"node"`), []byte(`"side":"coordinator","side":"node"`), 1)
					return b.Put([]byte(primary), raw)
				default:
					bad := m
					if fault == "source" {
						bad.SourceID = "foreign-source"
					} else {
						bad.LocalPolicyVersion++
					}
					return put(tx, clientMappingBucket, primary, clientMappingRecord{ClientMappingNode, bad})
				}
			})
			_ = db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(copyPath, j.Identity()); err == nil {
				_ = reopened.Close()
				t.Fatal("corrupt original mapping evidence reopened")
			}
		})
	}
}
