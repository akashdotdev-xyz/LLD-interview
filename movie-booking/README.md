# Movie Booking (BookMyShow): LLD Interview Walkthrough

Run it:
```
go run ./cmd                 # demo
go test -race ./...          # tests
```

The catalog and search part is easy. Interviewers grade **how you stop two people booking the same
seat**, **how you handle a seat that's held but not paid for**, and whether you keep the payment call
outside the lock.

---

## Step 1: Clarify requirements (≈5 min)

| Question | Assumption here | Why it matters |
|---|---|---|
| Who adds cinemas and shows? | An admin, through the same service | Catalog writes are rare, reads are constant |
| Can one screen have two shows at once? | No, overlapping shows are rejected | Validation in `AddShow` |
| Seat types and prices? | Regular / Premium / Recliner, price set per show | Price lives on the show, not the seat |
| Pick seats, or "any 3 seats"? | User picks exact seats | All-or-nothing hold |
| How long to pay? | Hold seats for 5 min, then they go back on sale | **Hold with expiry** |
| Can payment fail? | Yes, and the user can retry while the hold lasts | Separate PENDING / PAYING / CONFIRMED |
| Max seats per booking? | 10 | Stops one user holding a whole show |
| Cancellation? | Until the show starts, full refund | Refund policy could become a strategy |
| Out of scope | Users/auth, coupons, food, persistence, seat-map UI | Say this out loud |

**Functional**
1. `AddCinema`, `AddShow`: admin adds cinema halls and shows.
2. `CinemasShowing(city, movie)`: cinemas in a city showing a movie.
3. `ShowsAt(cinema, movie)`: upcoming shows of a movie at a cinema.
4. `AvailableSeats(show)` → `HoldSeats(user, show, seats)` → `Confirm(booking)` (pays).
5. `Cancel(booking)`.

**Non-functional**
- A seat must never be **sold twice**. A user must never be **charged twice**.
- Searches are fast and stay correct when shows are added.
- Booking show X never waits on someone booking show Y.

---

## Step 2: Core entities (≈5–10 min)

| Entity | Responsibility |
|---|---|
| `Movie` | ID, title, duration |
| `Cinema` | ID, name, city, screens. Never changes once added |
| `Screen` | Fixed seat layout |
| `Seat` | ID + `SeatType`. **No status**: the same seat is free at 6pm and booked at 9pm |
| `Show` | Movie on a screen at a time, with prices. Owns the seat state for that screening |
| `Booking` | User, show, seats, amount, `BookingStatus`, hold expiry |
| `ShowObserver` | Told about every new show. `CityIndex` and `ShowIndex` implement it. **Observer pattern** |
| `PaymentProcessor` | Charges the user; external, slow, can fail |
| `Clock` | Injected time, so hold expiry is testable |
| `Service` | Facade the apps call; owns the catalog and the locks |

```
          admin                              user
            │ AddShow                          │ HoldSeats / Confirm / Cancel
   ┌────────▼───────────────────────────────────▼────────┐
   │                      Service                        │
   │  mu: cinemas, shows, bookings, observers            │
   └──┬──────────────┬───────────────────────────┬───────┘
      │ notifies     │ owns 1..*                 │ uses
 ┌────▼──────────┐ ┌─▼───────────────────────┐ ┌─▼────────────────┐
 │«ShowObserver» │ │ Show (showState)        │ │«PaymentProcessor»│
 │ CityIndex     │ │ mu, holder: seat→Booking│ └──────────────────┘
 │ ShowIndex     │ └─┬───────────────┬───────┘
 └───────────────┘   │ layout of     │ 0..*
              ┌──────▼─────┐   ┌─────▼────┐
              │ Screen/Seat│   │ Booking  │
              └────────────┘   └──────────┘
```

> **Common mistake:** a `status` field on `Seat`. A screen has the same seats for every show, so seat
> status has to be per show. Say: *"Seat is layout, the show owns availability."*

---

## Step 3: Key design decisions (the core of the interview)

### 3a. Search: Observer keeps the indexes up to date

Without indexes, "cinemas in Pune showing Dune" scans every show in the country. So keep two maps:

| Index | Shape | Answers |
|---|---|---|
| `CityIndex` | city → movie → cinema ID → cinema | Cinemas in a city showing a movie |
| `ShowIndex` | cinema → movie → shows sorted by start | Upcoming shows of a movie at a cinema |

They must change whenever a show is added. Calling them directly from `AddShow` works, but then
`AddShow` knows about every index. Instead each index is a `ShowObserver`, and `AddShow` notifies
whoever subscribed. Adding a third listener (push notifications, a recommendations feed) means writing
a new observer; `AddShow` doesn't change. The demo adds a `notifier` this way.

Observers are called **after** `mu` is released, so a slow observer can't block bookings.
See [search.go](internal/booking/search.go).

### 3b. Booking is two steps: hold, then pay

Payment takes the user a minute or two. If seats were only taken after payment, two users would pay for
A1 and one would need a refund. If they were taken for good at selection, someone who closes the tab
would block A1 forever. So:

```
HoldSeats → PENDING, seats held until ExpiresAt (5 min)
Confirm   → PAYING → CONFIRMED          (or back to PENDING if the card is declined)
             nobody paid in time        → EXPIRED, seats back on sale
Cancel    → CANCELLED, seats back on sale, refund if it was confirmed
```

**Expiry is lazy.** No timer, no background goroutine. A seat counts as free if its holder is
`CANCELLED`/`EXPIRED`, or `PENDING` past `ExpiresAt` (`seatFree` in [show.go](internal/booking/show.go)).
The booking is marked `EXPIRED` the next time anyone looks at it (`refresh` in
[booking.go](internal/booking/booking.go)). Say: *"I don't need a scheduler to free seats; the check
is the clock."* A sweeper is only needed if something must happen at expiry, like emailing the user.

### 3c. Concurrency: two users, one seat

```
User A: A1 free? yes            User B: A1 free? yes
User A: hold A1                 User B: hold A1   ❌ sold twice
```

**Check and hold must be one atomic step**, for all the seats at once. `HoldSeats` checks every seat,
then holds every seat, while holding the **show's** lock. If any seat is taken, nothing is held.

Why a lock per show and not one global lock: a blockbuster's opening night has thousands of users on
one show, and they shouldn't slow down someone booking a different movie. Seats of different shows never
interact, so the show is the natural lock boundary. `Service.mu` only guards the catalog maps, and is held
for a map lookup.

Lock order: a show's lock can be held while taking `mu` (to add the booking to the index), never the other way.

If the interviewer pushes on scale:
- **Lock per seat**: more parallel, but holding 3 seats means 3 locks taken in a fixed order (sort seat IDs) to avoid deadlock. Usually not worth it.
- **Database**: `UPDATE show_seat SET booking_id=?, hold_until=? WHERE show_id=? AND seat_id IN (...) AND (booking_id IS NULL OR hold_until < now())`, in a transaction, and check rows affected = number of seats.
- **Redis**: `SET seat:{show}:{seat} {booking} NX EX 300` per seat; the TTL is the hold expiry.

### 3d. Payment happens outside the lock, and can't lose the seats

`Charge` is a network call. Holding the show's lock during it would freeze that show for everyone. So
`Confirm` runs in three steps:

```
lock   │ hold still valid? status = PAYING
unlock │ payments.Charge(paymentID, amount)   ← others keep booking this show
lock   │ CONFIRMED, or back to PENDING on failure
```

`PAYING` does two jobs:
1. **No double charge.** A second `Confirm` (double tap, retry) gets `ErrPaymentInProgress`. A `Confirm` on an already confirmed booking returns it without charging.
2. **The hold can't expire mid-payment.** `PAYING` keeps the seats even past `ExpiresAt`. Without it, the hold could time out while the bank is thinking, another user grabs A1, and then the first user's payment succeeds for a seat they no longer have. Tested in `TestHoldCannotExpireDuringPayment`.

Each attempt gets its own payment ID (`B-4-1`, `B-4-2`) for the provider's **idempotency key**. Reusing
the booking ID would make the provider return the cached decline on retry.

### 3e. Smaller details interviewers notice

1. **Money is `int64` cents.** Prices are per show and seat type, so a Friday night can cost more.
2. **No overlapping shows on a screen**: `start < other.End && other.Start < end`.
3. **Every seat type on the screen needs a price**, checked in `AddShow`, so a booking can't cost 0.
4. **Return values are copies.** `Booking.clone()` copies `SeatIDs`; `AddCinema` copies the screens.
5. **Started shows** can't be held or cancelled, and `ShowsAt` hides them.
6. **Errors are named values** (`ErrSeatUnavailable`, `ErrHoldExpired`…), so the app can show the right message.
7. **Singleton?** Not needed here. Nothing breaks if a test makes its own `Service`, and with several servers
   the database is the source of truth anyway. If asked, the same `sync.Once` pattern as the parking lot applies.

---

## Step 4: Design patterns used (name them out loud)

- **Observer**: `ShowObserver`; the search indexes subscribe to new shows.
- **Facade**: `Service` is the only thing the apps talk to.
- **State** (light): `BookingStatus` with explicit transitions.
- **Dependency injection / functional options**: `WithPayments`, `WithClock`, `WithHoldTTL`.
- **Adapter**: `PaymentFunc` lets a plain function act as a `PaymentProcessor`.
- (If asked) **Strategy**: pricing (weekend, surge) and refund policy (full refund until 24h before, 50% after).
- (If asked) **Factory/Builder**: build a screen's seat layout from rows × columns config.

---

## Step 5: Follow-up questions to prepare

| Question | Answer sketch |
|---|---|
| Dynamic pricing (weekend, filling fast)? | `PricingStrategy.Price(show, seat, now)` called in `HoldSeats`. The price is fixed at hold time, so the user pays what they saw. |
| Refund policy by time left? | `RefundPolicy.Refund(booking, now)` strategy instead of the hard-coded full refund. |
| Actually issuing the refund? | It's a network call like `Charge`: mark `CANCELLED` + `RefundPending` under the lock, call the provider without it, retry until it succeeds (idempotency key = booking ID). |
| Payment provider hangs forever? | Pass a context with a timeout to `Charge`. On timeout, ask the provider by payment ID whether it went through before going back to `PENDING`. |
| Show removed or rescheduled? | Add `ShowRemoved` to the observer interface; indexes remove it; confirmed bookings get refunded and notified. |
| City index lists a cinema after its last show ended? | Store a count of upcoming shows per (city, movie, cinema), decrement on `ShowEnded`/`ShowRemoved`, drop at 0. |
| "Give me 4 seats together"? | Scan rows for 4 consecutive free seats under the show lock, then hold them in the same step. |
| Waitlist for a sold-out show? | When a hold expires or a booking is cancelled, offer the seats to the next user in a per-show queue. Needs a sweeper, since lazy expiry alone doesn't notice. |
| Millions of users at a release (flash sale)? | Virtual waiting room in front; shard shows across servers; seat holds in Redis with TTL; DB for confirmed bookings only. |
| Several servers? | The per-show mutex only works in one process. Move seat state to the DB or Redis and make the hold a conditional update (see 3c). |
| Persist across restarts? | Tables `cinema`, `screen`, `seat`, `show`, `show_seat(show_id, seat_id, booking_id, hold_until)`, `booking`. Lazy expiry works unchanged: `hold_until < now()` means free. |

---

## Practice plan

1. Read in this order: `cinema.go` → `show.go` → `booking.go` → `search.go` → `payment.go` → `service.go`.
2. Close the files. Re-write in **≤ 45 min**: catalog + `AddShow` first, then the two indexes as observers,
   then `HoldSeats` with one lock, then `Confirm` with `PAYING` outside the lock, then `Cancel`.
3. Explain Step 3 out loud, especially 3b, 3c and 3d. Those are where most candidates lose points.
4. Pick 2 follow-ups and implement them (dynamic pricing and "N seats together" are the most common).
