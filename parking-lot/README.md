# Parking Lot: LLD Interview Walkthrough

Run it:
```
go run ./cmd                 # demo
go test -race ./...          # tests
```

This is the most common LLD question. Because everyone knows it, interviewers grade
**how you scope it, how you model it, and how you handle concurrency**, not whether you finish.

---

## Step 1: Clarify requirements (≈5 min)

Ask before drawing anything. Each answer changes the design.

| Question | Assumption here | Why it matters |
|---|---|---|
| Vehicle types? | Motorcycle, Car, Truck | Drives the fit rules |
| Spot sizes? | Small, Medium, Large | |
| Can a car use a large spot? | Yes, but only if no medium is free | Fit rule + allocation strategy |
| Multiple floors? Multiple gates? | Yes, yes | Multiple gates means **concurrency** |
| How is the fee computed? | Hourly per vehicle type, per started hour, 1h minimum | Pricing strategy |
| Pay where? Can payment fail? | At a kiosk **or** the exit gate; yes it can fail | Paying and leaving are separate steps |
| Time to leave after paying? | 15 min grace, then extra time is charged | Grace period |
| Display boards? | Free count per floor and size | |
| Out of scope | Reservations, EV charging, persistence, monthly passes | Say this out loud |

**Functional**
1. `Park(vehicle)` → ticket with an assigned spot, or "lot full".
2. `AmountDue(ticket)` → what the kiosk or exit gate shows.
3. `Pay(ticket)` → charge what's due now. Can be called more than once.
4. `Exit(ticket)` → open the barrier and free the spot, only if nothing is owed.
5. `Available()` → free spots per floor and size.

**Non-functional**
- Two gates must never get the **same spot**. A ticket must never be **charged twice**.
- Finding a spot must be fast. Don't scan 10,000 spots per car.
- Easy to change pricing or allocation rules without editing `ParkingLot`.

---

## Step 2: Core entities (≈5–10 min)

| Entity | Responsibility |
|---|---|
| `Vehicle` | Plate + `VehicleType`. Plain data, **no inheritance** needed |
| `ParkingSpot` | ID, floor, number (distance from entrance), `SpotSize` |
| `Floor` | Owns its spots; one min-heap of **free** spots per size |
| `Ticket` | Vehicle, spot, entry/exit time, `TicketStatus` (`ACTIVE → EXITED`), list of payments |
| `Payment` | One charge attempt: amount, `PaymentStatus` (`PENDING → SUCCEEDED / FAILED`) |
| `AllocationStrategy` | Picks a spot: `BestFit`, `NearestFirst`. **Strategy pattern** |
| `PricingStrategy` | Computes the fee: `HourlyPricing`. **Strategy pattern** |
| `PaymentProcessor` | Charges the driver; external, slow, can fail |
| `Clock` | Injected time, so fee tests don't wait for hours |
| `ParkingLot` | **Singleton** facade the gates call; owns the lock and ties it all together |

```
                 ┌───────────────────────────────┐
  entry gate ──▶ │      ParkingLot (1 only)      │ ◀── kiosk / exit gate
                 │ mu, tickets, byPlate          │
                 └──┬──────────┬──────────┬──────┘
                    │ uses     │ uses     │ uses
       ┌────────────▼──────┐ ┌─▼────────────────┐ ┌──────────────────┐
       │      Floor        │ │«AllocationStrat.»│ │ «PricingStrategy»│
       │ free: size → heap │ │ BestFit          │ │ HourlyPricing    │
       │                   │ │ NearestFirst     │ └──────────────────┘
       └────────┬──────────┘ └──────────────────┘ ┌──────────────────┐
                │ holds                           │«PaymentProcessor»│
                │                                 └──────────────────┘
         ┌──────▼─────┐   ┌──────────┐ 1..* ┌──────────┐
         │ParkingSpot │◀──│  Ticket  │─────▶│ Payment  │
         └────────────┘   └──────────┘      └──────────┘
```

> **Common mistake:** `class Car extends Vehicle`, `class Truck extends Vehicle`, `class CompactSpot extends Spot`…
> The subclasses have no different behaviour, only a different size. An enum + a fit table is simpler and
> adding a vehicle type is one line. Say: *"I'd only use inheritance if types had different behaviour."*

---

## Step 3: Key design decisions (the core of the interview)

### 3a. Fit rules and which spot to pick

[vehicle.go](internal/parkinglot/vehicle.go): `fittingSizes` returns the sizes a vehicle can use, smallest first.

Then *which* fitting spot? That's a business choice, so it's a strategy:

| Strategy | Car arrives, floor 1 has only a large spot, floor 2 has a medium |
|---|---|
| `BestFit` (default) | Floor 2 medium. Keeps the large spot for a truck |
| `NearestFirst` | Floor 1 large. Shorter walk, but a truck may be turned away later |

Bringing this trade-off up yourself is a strong signal.

### 3b. Finding a free spot fast

| Approach | Cost per car |
|---|---|
| Loop over every spot, check `isFree` | O(total spots) |
| Free list per size per floor | O(1), but not "nearest" |
| **Min-heap of free spots per size per floor, by spot number** ✅ | O(1) peek, O(log n) take/release, always nearest |

Each spot stores its heap `index` (updated in `Swap`), so the lot can remove *any* spot a strategy returns,
not just the top. See [spot.go](internal/parkinglot/spot.go), [floor.go](internal/parkinglot/floor.go).

### 3c. Concurrency: two gates, one spot

```
Gate A: find → spot 7 free        Gate B: find → spot 7 free
Gate A: take spot 7               Gate B: take spot 7   ❌ double booked
```

**Find and take must be one atomic step.** `Park` does both while holding `mu`
([parking_lot.go](internal/parkinglot/parking_lot.go)). The strategy only *looks*; the lot *takes*.


Why one global lock is fine: everything under it is a map lookup or a heap operation (microseconds).
A real lot has maybe 10 gates and a car every few seconds. If the interviewer pushes:
- **Lock per floor**: gates mostly hit different floors. But `BestFit` looks at all floors, so you need a
  consistent lock order (floor 1, 2, 3…) to avoid deadlock, or "try floor, lock it, re-check, take".
- **CAS per spot** (`atomic.Bool` / `AtomicBoolean`): lock-free, but you lose the "nearest" heap.
- **Database**: `UPDATE spot SET vehicle=? WHERE id=? AND vehicle IS NULL`, and check rows affected = 1.

### 3d. Ticket status and payment status are separate

A first attempt often uses one field: `ACTIVE → PAYMENT_PENDING → PAID`. That mixes two lifecycles:

| Ticket (where is the vehicle?) | Payment (where is the money?) |
|---|---|
| `ACTIVE → EXITED` | `PENDING → SUCCEEDED` or `FAILED` |
| One per visit | Many per ticket |

Cases that break the single field:
- **Pay at a kiosk, walk to the car, drive out.** The ticket is paid but the car is still parked.
- **Declined card, then cash.** Two attempts. A failed one is kept for audits and disputes instead of being overwritten.
- **Paid, but left after the grace period.** A second payment for the extra time.
- **Refunds and partial payments** belong to a payment, not a ticket.

Say: *"Parking and payment are separate state machines, because one ticket can have many payment
attempts, and being paid isn't the same as having left."* See [ticket.go](internal/parkinglot/ticket.go),
[payment.go](internal/parkinglot/payment.go).

The amount due is always worked out, never stored: `fee(entry → billUntil) − sum(succeeded payments)`.
`billUntil` is the time of the last payment if we're still inside the grace period, otherwise now.

### 3e. Payment happens outside the lock

`PaymentProcessor.Charge` is a network call. Holding the global lock during it would stop **every** gate
for seconds. So `Pay` runs in three steps:

```
lock   │ amount = amountDue(now); add Payment{PENDING}
unlock │ payments.Charge(payment.ID, amount)   ← other gates keep working
lock   │ payment → SUCCEEDED or FAILED
```

While a payment is `PENDING`, another `Pay` or an `Exit` on the same ticket gets `ErrPaymentInProgress`.
That's what stops a double charge, and stops the car leaving before the money clears.
Each attempt has its own ID, which the provider uses as an **idempotency key**: if the request is retried
after a timeout, the provider won't charge again. Tested in `TestConcurrentPayChargesOnce` and
`TestSlowPaymentDoesNotBlockEntry`.

### 3f. Singleton: one lot per process

There's one physical lot, and every gate must see the same spots. If two gates each built their own
`ParkingLot`, both would think spot 7 is free. So the app gets the lot through `GetInstance`:

```go
var (
    instance     *ParkingLot
    instanceErr  error
    instanceOnce sync.Once
)

func GetInstance(floors []FloorConfig, opts ...Option) (*ParkingLot, error) {
    instanceOnce.Do(func() { instance, instanceErr = NewParkingLot(floors, opts...) })
    return instance, instanceErr
}
```

What to say about it:
1. **It must be thread-safe.** The naive `if instance == nil { instance = New() }` is a race: two gates
   starting at once can each create a lot. `sync.Once` runs the body exactly once, and other callers wait for
   it to finish. The Java equivalents are double-checked locking with a `volatile` field, the holder-class idiom,
   or an `enum` singleton.
2. **Lazy, not eager.** The lot is built on first use, after config is loaded. A package-level
   `var instance = New(...)` would run before `main` and couldn't take config.
3. **Later arguments are ignored.** Only the first call's floors and options are used. Say this out loud;
   it's the classic singleton trap. The alternative is `Init(...)` once in `main` plus `Instance()` everywhere else.
4. **Testability.** A global instance means tests share state. That's why the dependencies are injected
   (clock, payments, strategies) and `NewParkingLot` is still exported: tests build their own lot, and only
   `TestGetInstanceReturnsOneLot` touches the real singleton. In Java you'd hide the constructor and add a
   reset hook for tests. Say *"singleton for how the app reaches the lot, dependency injection for what's inside it."*
5. **Its limit.** It's one per **process**. With several servers, the database is the single source of truth,
   not the singleton (see follow-ups).

### 3g. Smaller details interviewers notice

1. **Money is `int64` cents, never float.** `0.1 + 0.2 != 0.3`.
2. **Inject the `Clock`.** Otherwise you can't test "2h10m costs 3 hours" without waiting.
3. **Same plate can't park twice** (`byPlate` map). Catches lost-ticket fraud and bugs.
4. **Return `Ticket` by value, and copy its `Payments` slice.** A struct copy still shares the slice's
   array, so without `clone()` a caller could change the lot's payments without the lock.
5. **Errors are named values** (`ErrLotFull`, `ErrAlreadyParked`…), so gates can show the right message.

---

## Step 4: Design patterns used (name them out loud)

- **Singleton**: `GetInstance` with `sync.Once`, one lot per process.
- **Strategy**: `AllocationStrategy`, `PricingStrategy`. New rules = new type, `ParkingLot` unchanged (Open/Closed).
- **Facade**: `ParkingLot` is the only thing gates talk to.
- **State** (light): two small state machines, `TicketStatus` and `PaymentStatus`.
- **Dependency injection / functional options**: `WithStrategy`, `WithPricing`, `WithPayments`, `WithClock`.
- **Adapter**: `PaymentFunc` lets a plain function act as a `PaymentProcessor`.
- (If asked) **Observer**: display boards subscribe to "spot taken/freed" events instead of polling `Available()`.
- (If asked) **Factory**: create spots/floors from config.

---

## Step 5: Follow-up questions to prepare

| Question | Answer sketch |
|---|---|
| Different pricing: first hour free, weekend rates, daily cap? | New `PricingStrategy`, or a chain of rules (each adjusts the fee). Nothing else changes. |
| EV spots with chargers? | Spot gets a `Features` set; a vehicle's request includes required features; strategy filters. Separate heaps per (size, feature). |
| Reservations? | Spot state `RESERVED` with expiry; reserved spots are removed from free heaps; a timer (see task-scheduler!) releases expired ones. |
| Lost ticket? | Look up by plate (`byPlate`), charge a lost-ticket fee as a normal `Payment`. |
| Refunds? | Add `REFUNDED` to `PaymentStatus` (or a negative `Payment`); `PaidCents` already sums only what counts. |
| Split payment (part card, part cash)? | Already works: `Pay` charges what's due; add an amount argument to pay part of it. |
| Spot under maintenance? | `occupy` it with no ticket, or an `OutOfService` flag that keeps it out of the free heap. |
| Display boards in real time? | Observer: lot publishes events; each board keeps its own counts. |
| Multiple lots / city-wide? | The singleton becomes a registry: `map[lotID]*ParkingLot`, or one service per lot with its own DB; a lookup service aggregates availability. |
| Several servers for one lot? | A per-process singleton no longer guarantees one lot. Move state to a database and make taking a spot a conditional update (see 3c). |
| Persist across restarts? | Tables `spot`, `ticket`, `payment`; rebuild free heaps from `spot WHERE vehicle IS NULL` on startup. Put storage behind a `Repository` interface so `ParkingLot` doesn't change. |
| Payment crashes mid-way? | The `PENDING` payment is persisted; on restart, ask the provider by payment ID (the idempotency key) whether it went through, then mark it `SUCCEEDED` or `FAILED`. |
| Very high traffic? | Lock per floor or per (floor, size); or shard by floor. Measure first. |

---

## Practice plan

1. Read in this order: `vehicle.go` → `spot.go` → `floor.go` → `strategy.go` → `pricing.go` → `ticket.go` → `payment.go` → `parking_lot.go`.
2. Close the files. Re-write in **≤ 45 min**: start with one floor and a linear scan, then add heaps,
   then strategies, then `Pay`/`Exit` with separate payment status, then the singleton.
3. Explain Step 3 out loud, especially 3c, 3d, 3e and 3f. Those are where most candidates lose points.
4. Pick 2 follow-ups and implement them (pricing rules and reservations are the most common).
