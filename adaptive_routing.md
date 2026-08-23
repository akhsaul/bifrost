# Implementation Plan — Adaptive Routing & Dynamic Load Balancing (OSS)

## Goal Description
Implement an **Adaptive Routing & Dynamic Load Balancing** subsystem in the open-source Bifrost gateway that automatically optimizes traffic distribution across LLM providers, models, and API keys based on real-time performance metrics (Time-to-First-Token, end-to-end latency, error rates, and 429 rate limits). This bridges the gap between static rule-based routing and runtime network realities, ensuring resilience against provider slowdowns, regional outages, and rate-limiting.

---

## Architectural Principles & Guarantees

> [!IMPORTANT]
> **Key Architectural Guarantees**:
> 1. **Zero-Lock Hot-Path Read (< 1µs)**:
>    - Route selection at request time reads from an immutable snapshot (`atomic.Pointer[AdaptiveRoutingSnapshot]`).
>    - Zero mutex contention during high-concurrency request dispatch (5,000+ RPS).
> 2. **Dual-Level Adaptation**:
>    - **Provider/Model Level (Routing Engine Target Group)**: Automatically distributes load among equivalent candidate providers (e.g., `openai/gpt-4o`, `azure/gpt-4o`, `groq/llama-3.3-70b`).
>    - **Key Level (Key Selector)**: Optimizes key selection within the chosen provider pool.
> 3. **Dual Storage Support (In-Memory + Redis)**:
>    - **In-Memory Store (Default)**: Ultra-fast atomic ring buffers for single-instance deployments.
>    - **Redis Store (Distributed)**: Compatible with multi-replica Bifrost clusters using sliding time-bucket aggregations.
> 4. **Exploration Floor ($\epsilon$-Greedy)**:
>    - Degraded routes maintain a minimum traffic allocation (e.g. 5%) to probe latency recovery and prevent provider starvation.

---

## Architecture Diagram

```mermaid
graph TD
    subgraph Hot Path (PreLLMHook / KeySelector)
        Req[1. Incoming Request] --> Gov[Governance & Rules Filter<br/>Hard Boundary: Budget, Geo, Tier]
        Gov --> Candidates[Candidate Providers / Models / Keys]
        Candidates --> Snap[2. Read Active Snapshot<br/>atomic.Pointer / Zero Lock]
        Snap --> Selector[3. O(1) Dynamic Weighted Choice]
        Selector --> ProviderExec[4. Provider Queue & Execution]
    end

    subgraph Async Telemetry Ingestion (PostLLMHook)
        ProviderExec --> Resp[Response / Stream TTFT / Error]
        Resp --> TelemetryHook[5. PostLLMHook Ingestion]
        TelemetryHook --> Store[(Metrics Store<br/>In-Memory Ring Buffer / Redis)]
    end

    subgraph Background Tuning Worker (Every 2-5s)
        Store --> Tuner[Adaptive Tuner Loop]
        Tuner --> EWMA[Calculate EWMA Latency, TTFT & Error Penalty]
        EWMA --> WeightCalc[Dynamic Inverse-Latency Weighting + Exploration Floor]
        WeightCalc --> NewSnap[Generate New Immutable Snapshot]
        NewSnap -.->|Atomic Swap| Snap
    end
```

---

## Proposed Changes

### Component 1: Metrics Storage & Statistics (`core/routing/adaptivetracker/`)

#### [NEW] `types.go` & `store.go`
- **`MetricsStore` Interface**:
  - `RecordMetric(ctx, target TargetID, duration time.Duration, ttft time.Duration, isError bool, isRateLimit bool)`
  - `GetStats(ctx, target TargetID, window time.Duration) TargetStats`
  - `GetAllStats(ctx, window time.Duration) map[TargetID]TargetStats`
- **`InMemoryMetricsStore`**:
  - Lock-free ring buffer per target with atomic counters for fast telemetry ingestion.
- **`RedisMetricsStore`**:
  - Redis sliding bucket / hash metrics store for distributed Bifrost nodes.

#### [NEW] `score.go`
- **Scoring & Weight Formulation**:
  - **Latency Metric**: Blended EWMA of TTFT (for streams) and Total Latency (for unary).
  - **Penalty Multiplier**: Heavy penalization for HTTP 429 (Rate Limit) and 5xx (Down) responses.
  - **Inverse-Latency Weighting**:
    $$W_i = \frac{1}{(\text{EffectiveLatency}_i)^k} \times \text{SuccessRate}_i$$
  - **Exploration Floor**: Ensure $\text{Weight}_i \ge \epsilon \times \sum W$ so degraded providers can test recovery.

---

### Component 2: Background Weight Tuner & Hot-Path Selector (`core/routing/adaptiveselector/`)

#### [NEW] `snapshot.go`
- Holds the immutable pre-computed routing weights:
  ```go
  type TargetWeight struct {
      TargetID  string  // e.g., "openai/gpt-4o" or key UUID
      Weight    float64
      CumWeight float64
      Score     float64
      P90       time.Duration
  }

  type AdaptiveRoutingSnapshot struct {
      Groups    map[string][]TargetWeight // Pool/Rule ID -> Weighted Targets
      UpdatedAt time.Time
  }
  ```

#### [NEW] `tuner.go`
- Background worker executing periodically (default: 3 seconds):
  1. Pulls statistics from `MetricsStore`.
  2. Recomputes composite scores and normalized distribution weights.
  3. Builds a new `AdaptiveRoutingSnapshot`.
  4. Performs `atomic.StorePointer` to update the hot-path snapshot.

#### [NEW] `selector.go`
- Provides fast, zero-allocation selection:
  ```go
  func (s *AdaptiveSelector) PickTarget(groupID string) (string, error)
  ```

---

### Component 3: Integration with Governance & Core Routing

#### [MODIFY] `framework/configstore/tables/routingrules.go` & `plugins/governance/routing.go`
- Support **Target Routing Strategies**:
  - Support `strategy: "adaptive" | "weighted" | "priority"` in routing rule target groups.
  - When evaluating candidate targets for a rule with `strategy: "adaptive"`, consult `AdaptiveSelector` to dynamically choose the optimal target provider/model.

#### [MODIFY] `core/keyselectors/` & `core/schemas/bifrost.go`
- Integrate adaptive scoring into the provider `KeySelector` chain so that single-provider multi-key setups also benefit from latency-based key picking.

#### [NEW] `plugins/adaptiverouting/`
- Implements `LLMPlugin` (`PreLLMHook` & `PostLLMHook`):
  - Ingests duration, TTFT, and error status into the `MetricsStore` asynchronously.

---

### Component 4: REST API & Telemetry Handlers (`transports/bifrost-http/handlers/`)

#### [NEW] `adaptiverouting.go`
- `GET /api/v1/adaptive-routing/metrics`: Real-time view of EWMA latency, error penalties, and current weights.
- `GET /api/v1/adaptive-routing/flow`: Target distribution statistics for visual Sankey charts.
- `POST /api/v1/adaptive-routing/config`: Dynamic configuration updates (exploration floor $\epsilon$, decay factor $\alpha$, window size).

---

### Component 5: Web UI Dashboard (`ui/app/workspace/adaptive-routing/`)

#### [MODIFY] `page.tsx` & Components
- **Live Traffic Flow (Sankey Diagram)**: Real-time dynamic visual routing flow from Request $\to$ Provider $\to$ Key.
- **Latency & Error Score Matrix**: Live table showing Base Weights vs Effective Dynamic Weights.
- **Real-time EWMA & P90 Latency Graphs**: Visual trends of provider response times.
- **Configuration Drawer**: UI controls for sensitivity tuning, min exploration rate, and fallback behavior.

---

## Verification & Testing Plan

### Automated Tests
1. **Unit & Mathematical Verification**:
   - `core/routing/adaptivetracker/score_test.go`: Test EWMA calculation, error penalties, and exploration floor.
   - `core/routing/adaptiveselector/snapshot_test.go`: Test zero-lock concurrent reads and atomic swaps.
2. **Simulation & Flapping Resilience Test**:
   - Simulate 5,000 concurrent requests with Mock Provider A (fast 80ms) and Mock Provider B (slow 1,200ms).
   - Verify traffic shifts $>85\%$ to Provider A within 3 seconds.
   - Simulate Provider A throwing 429s/500s; verify traffic smoothly falls back to Provider B without thundering herd issues.
   - Simulate Provider A recovery; verify smooth ramp-up via exploration floor traffic.
3. **Multi-Replica Redis Store Test**:
   - Test Redis metrics aggregation across simulated multi-instance nodes.

### Manual Verification
1. Run local dev server with `make dev`.
2. Access `http://localhost:3000/workspace/adaptive-routing`.
3. Send concurrent requests with differing latency delays to verify dynamic weight shifting on the UI Sankey diagram and metrics matrix.
