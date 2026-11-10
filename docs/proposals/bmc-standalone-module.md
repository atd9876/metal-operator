# Extract `bmc` Package into a Standalone Go Module

> Implements [#1205](https://github.com/ironcore-dev/metal-operator/issues/1205).
> IEP: [ironcore-dev/enhancements#74](https://github.com/ironcore-dev/enhancements/pull/74)

---

## 1. Problem Statement

The `bmc` package lives inside `metal-operator`, which creates artificial coupling with
`metal-maintenance-operator` (MMO):

- MMO must declare a full dependency on `metal-operator` just to consume the BMC client
  library, even though it needs none of MO's API types or controllers for that purpose.
- `k8s.io/api v0.36` (required by MO) conflicts with `k8s.io/api v0.37` (required by MMO),
  causing transitive version skew that requires `replace` directives or pinned pseudo-versions.
- Any breaking change in unrelated MO APIs can break MMO's build.
- Third-party vendors cannot implement or extend BMC clients without pulling in MO's entire
  operator stack.

---

## 2. Proposal

Create a new repository `github.com/ironcore-dev/bmc` containing the extracted package as a
standalone Go module with minimal dependencies.

### 2.1 Repository layout

```
github.com/ironcore-dev/bmc/
├── go.mod                    # module github.com/ironcore-dev/bmc
├── go.sum
├── LICENSE
├── LICENSES/
│   ├── Apache-2.0.txt
│   └── BSD-3-Clause.txt      # for mock/server/data (DMTF-contributed fixtures)
├── REUSE.toml
├── CODEOWNERS
├── README.md
├── Makefile
├── .golangci.yml
├── .custom-gcl.yml
├── .github/
│   └── workflows/
│       ├── test.yml
│       ├── lint.yml
│       ├── reuse.yml
│       └── release-drafter.yml
├── hack/
│   └── boilerplate.go.txt
│
├── *.go                      # package bmc (root package — no subdirectory nesting)
│   bmc.go
│   redfish.go
│   redfish_dell.go
│   redfish_fujitsu.go
│   redfish_hpe.go
│   redfish_lenovo.go
│   redfish_supermicro.go
│   redfish_local.go
│   oem_helpers.go
│   *_test.go
│
├── mock/
│   └── server/
│       ├── server.go         # package server (standalone, no bmc import)
│       └── data/             # embedded Redfish JSON fixtures (BSD-3-Clause)
│
└── cmd/
    └── mockserver/
        └── main.go           # standalone mock server binary
```

The root package is `package bmc`, so consumers write:

```go
import "github.com/ironcore-dev/bmc"
```

This follows the convention of `gofish`, `go-logr/logr`, and other well-known Go libraries —
the import path is the module path, with no double-segment anti-pattern (`bmc/bmc/`).

### 2.2 Target dependencies

```
require (
    github.com/go-logr/logr        // logging
    github.com/stmcginnis/gofish   // Redfish transport
    // test-only:
    github.com/onsi/ginkgo/v2
    github.com/onsi/gomega
)
```

No `k8s.io/api`, no `k8s.io/client-go`, no `sigs.k8s.io/controller-runtime`. See §3 for
how the two current `k8s.io/apimachinery` usages are removed.

### 2.3 Versioning

Start at `v0.1.0`. No `/v2` import-path suffix is needed until a breaking change requires
it. MO and MMO pin to this tag in their `go.mod`.

---

## 3. Required Code Changes

### 3.1 Replace `ctrl.LoggerFrom` with `logr.FromContextOrDiscard`

`sigs.k8s.io/controller-runtime` is used in the `bmc` package only for `ctrl.LoggerFrom(ctx)`,
which is itself a thin wrapper around `logr.FromContextOrDiscard`. Replacing all call sites
removes controller-runtime from the module entirely.

Affected non-test production files (~12 call sites):

| File | Call sites |
|---|---|
| `redfish.go` | 5 |
| `oem_helpers.go` | 3 |
| `redfish_lenovo.go` | 1 |
| `redfish_supermicro.go` | 1 |
| `redfish_local.go` | 1 |

```go
// Before
import ctrl "sigs.k8s.io/controller-runtime"
log := ctrl.LoggerFrom(ctx)

// After
import "github.com/go-logr/logr"
log := logr.FromContextOrDiscard(ctx)
```

`cmd/mockserver/main.go` (the development binary) may retain `ctrl.SetLogger` / `zap`
setup — it is not a library and its dependencies do not propagate to consumers.

### 3.2 Drop `k8s.io/apimachinery` — two targeted changes

#### 3.2.1 `resource.Quantity` in `SystemInfo.TotalSystemMemory`

`SystemInfo.TotalSystemMemory` is currently typed as `resource.Quantity` because MO's
`server_controller.go` assigns it to `server.Status.TotalSystemMemory *resource.Quantity`
(a CRD field). The BMC package should not need to know about Kubernetes quantity formatting.
Raw bytes is the correct primitive here.

**Change `bmc/bmc.go`:**

```go
// Before
TotalSystemMemory resource.Quantity

// After
TotalSystemMemory int64 // bytes
```

**Change `bmc/redfish.go` — `GetSystemInfo`:**

```go
// Before
memoryString := fmt.Sprintf("%.fGi", gofish.Deref(system.MemorySummary.TotalSystemMemoryGiB))
quantity, err := resource.ParseQuantity(memoryString)
if err != nil {
    return SystemInfo{}, fmt.Errorf("failed to parse memory quantity: %w", err)
}
// ... TotalSystemMemory: quantity,

// After — no import, no error path
TotalSystemMemory: int64(gofish.Deref(system.MemorySummary.TotalSystemMemoryGiB) * 1024 * 1024 * 1024),
```

**Change MO `internal/controller/server_controller.go`** (the only consumer):

```go
// Before
server.Status.TotalSystemMemory = &systemInfo.TotalSystemMemory

// After — convert at the Kubernetes boundary
q := resource.NewMilliQuantity(systemInfo.TotalSystemMemory*1000, resource.BinarySI)
server.Status.TotalSystemMemory = q
```

MMO does not use `TotalSystemMemory` and requires no change.

#### 3.2.2 `wait.PollUntilContextTimeout` — replace with a stdlib helper

`k8s.io/apimachinery/pkg/util/wait` is used in three places in `redfish.go`, all with the
same call signature. A small private helper replicates the semantics (`immediate=true` means
check once before the first tick) using only `context`, `time`, and `errors` from the
standard library:

```go
// pollUntilContextTimeout polls condition every interval until it returns true,
// an error, or the timeout or ctx expires. If immediate is true the condition is
// checked once before the first tick.
func pollUntilContextTimeout(
    ctx context.Context,
    interval, timeout time.Duration,
    immediate bool,
    condition func(context.Context) (bool, error),
) error {
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()
    if immediate {
        if ok, err := condition(ctx); err != nil || ok {
            return err
        }
    }
    ticker := time.NewTicker(interval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            if ok, err := condition(ctx); err != nil || ok {
                return err
            }
        }
    }
}
```

The three call sites in `redfish.go` (`getSystemFromUri`, `GetStorages`,
`WaitForServerPowerState`) are updated to call this helper instead of `wait.PollUntilContextTimeout`.

---

## 4. The `pkg/bmcutils` problem

`metal-operator/pkg/bmcutils` is not part of the `bmc` extraction, but MMO's dependency on
it is just as problematic: MMO imports `pkg/bmcutils` for client-construction helpers
(`GetBMCClientFromBMC`, `GetBMCClientForServer`, `CreateBMCClient`, `GetBMCCredentialsFromSecret`,
`GetProtocolScheme`, `GetBMCAddressForBMC`, `GetServerNameFromBMCandIndex`,
`BMCUnAvailableError`, `BMCConnectivityCheckOption`) — forcing it to carry a full MO
dependency just for ~150 lines of glue code.

`pkg/bmcutils` cannot move into the new `bmc` module because it depends on `metalv1alpha1`
CRD types; that would re-introduce the Kubernetes coupling the extraction aims to remove. A
third shared module adds unnecessary release coordination overhead for code this simple.

**The right fix: MMO copies the relevant functions into its own
`internal/utils/bmcutils.go`.** The functions are straightforward glue between
`metalv1alpha1` (already a direct MMO dependency) and the `bmc.BMC` interface. Both repos
maintain independent copies; since `pkg/bmcutils` has been stable, drift is not a practical
concern. `SSHResetBMC` and `SSHResetBMCFunc` are not used by MMO and are not copied.

After this, MMO's only remaining dependency on `metal-operator` is `api/v1alpha1` (the CRD
types), which is both necessary and correct.

---

## 5. Implementation Steps

Steps must land in order — each step unblocks the next.

**Step 1 — Create `github.com/ironcore-dev/bmc` (new repo PR)**

- Scaffold the repository: `go.mod`, CI workflows, REUSE config, Makefile, `hack/boilerplate.go.txt`.
- Copy `bmc/*.go` and `bmc/mock/` from MO, update SPDX headers.
- Apply the §3 code changes: `ctrl.LoggerFrom` → `logr.FromContextOrDiscard`,
  `TotalSystemMemory resource.Quantity` → `int64`, `wait.PollUntilContextTimeout` →
  stdlib helper.
- Verify `go mod tidy` leaves only `go-logr/logr`, `gofish`, `ginkgo/v2`, `gomega` as
  direct dependencies.

**Step 2 — Tag `v0.1.0` on `github.com/ironcore-dev/bmc`**

- Run `go get github.com/ironcore-dev/bmc@v0.1.0` from a scratch module to confirm the
  dependency graph is clean before tagging.

**Step 3 — Update `metal-operator` (PR against MO)**

- Delete `bmc/` directory.
- Add `require github.com/ironcore-dev/bmc v0.1.0` to `go.mod`.
- Update import paths in: `cmd/main.go`, `internal/controller/bmc_controller.go`,
  `internal/controller/endpoint_controller.go`, `internal/controller/server_controller.go`,
  `internal/controller/suite_test.go`, `pkg/bmcutils/bmcutils.go`.
- Apply the `server_controller.go` quantity conversion from §3.2.1.

**Step 4 — Update `metal-maintenance-operator` (PR against MMO, depends on steps 2 and 3)**

- Add `internal/utils/bmcutils.go` containing the copied glue functions (see §4 above).
  Import alias in all callers changes from `"github.com/ironcore-dev/metal-operator/pkg/bmcutils"`
  to `"github.com/ironcore-dev/metal-maintenance-operator/internal/utils"` (already the
  package name used for `helper.go`, so no import alias change is needed in callers).
- Add `require github.com/ironcore-dev/bmc v0.1.0` to `go.mod`.
- Update all ~25 import paths from `github.com/ironcore-dev/metal-operator/bmc` →
  `github.com/ironcore-dev/bmc`.
- Drop the `github.com/ironcore-dev/metal-operator/pkg/bmcutils` import from all files;
  they now use the local copy.
- The `github.com/ironcore-dev/metal-operator` dependency in MMO's `go.mod` is retained —
  it is still needed for `api/v1alpha1` CRD types — but the transitive `k8s.io/api v0.36`
  conflict is resolved because the `bmc` package no longer pulls in MO's full dependency
  graph.

---

## 6. What Stays in `metal-operator`

**`pkg/bmcutils`** is not extracted. It bridges the `bmc` client with MO's CRD types and
belongs in MO. After step 3 it simply changes its own import from
`github.com/ironcore-dev/metal-operator/bmc` to `github.com/ironcore-dev/bmc`.

---

## 7. What Is Not Changing

- The `bmc` package's public API surface (interfaces, struct types, vendor implementations)
  is unchanged except for `SystemInfo.TotalSystemMemory: resource.Quantity → int64`.
- `mock/server` stays in the same module (not a separate `go.mod`). Splitting it would add
  versioning and `replace`-directive friction for almost no benefit, since test dependencies
  are not transitive to library consumers.
- No controller logic, CRD types, or Helm chart is moved.
