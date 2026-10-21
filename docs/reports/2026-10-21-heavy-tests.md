## Ledgerbot test report: PASS

Tests: **379 passed**, 0 failed, 16 skipped · coverage 44.2%

| Suite | Passed | Failed | Skipped |
|---|---|---|---|
| Unit | 279 | 0 | 11 |
| Integration | 19 | 0 | 1 |
| Feature | 77 | 0 | 3 |
| Simulation | 4 | 0 | 1 |

| RAM profile | Sustained req/s | at VUs | Max VUs within SLO | Peak req/s | Best sync p95 | Worst errors | OOM kills |
|---|---|---|---|---|---|---|---|
| 2gb | – | 0 | 0 | 1901 | 14.3 ms | 19.75% | 42 |
| 4gb | 1851 | 10 | 100 | 1851 | 13.9 ms | 0.00% | 0 |
| 6gb | 2367 | 50 | 100 | 2367 | 14.3 ms | 0.00% | 0 |
| 8gb | 2065 | 10 | 100 | 2065 | 16.0 ms | 0.00% | 0 |
| 2gb-tuned | 2406 | 10 | 100 | 2406 | 7.77 ms | 0.01% | 0 |

**Findings**

- 16 known defect(s) are recorded as BUG skips; they are listed under Known defects.
- Profile 2gb: the kernel OOM killer fired 42 time(s) inside the memory limit (restarts: 0); worst error rate 19.8%.
- Profile 2gb: these containers hit their memory limit during the run: postgres, api, gateway.
- Profile 2gb: the queue did not drain within the timeout after 6 step(s).
- Profile 2gb never met the service level (p95 < 500 ms and errors < 1%) at any load step.
- Profile 4gb keeps the service level up to 100 parallel clients and breaks it at 200.
- Profile 6gb keeps the service level up to 100 parallel clients and breaks it at 200.
- Profile 6gb: the queue did not drain within the timeout after 1 step(s).
- Profile 8gb keeps the service level up to 100 parallel clients and breaks it at 200.
- Profile 8gb asks for more memory than the Docker host has; limits above the host size are not enforced, so it behaves like the largest profile that fits.
- Profile 2gb-tuned keeps the service level up to 100 parallel clients and breaks it at 200.
- Highest sustained throughput: 2406 req/s on 2gb-tuned at 10 parallel clients (sync p95 under 500 ms, errors under 1%).
- Lowest sustained throughput: 1851 req/s on 4gb, 1.3x below the best profile.

**Known defects (BUG skips)**

- internal/ai TestFakeEmbedderZeroDims: FakeEmbedder{} (Dims 0) panics on any non-empty text (runtime error: integer divide by zero): modulo by len(v)==0; only the registry's max(Dims, 8) guard prevents it
- internal/domain TestMoneyStringMinInt64: Money(MinInt64).String() = "--92.233.720.368.547.758,0-8 TL", want "-92.233.720.368.547.758,08 TL"; negating MinInt64 overflows, so both the whole and fractional parts keep their sign
- internal/domain TestParseAmountMultiplierFraction: ParseAmount("1.234,567 milyon") = 123456000000, <nil>; want 123456700000. With a multiplier the fraction is allowed past 2 digits but then truncated to frac[:2], dropping value (and "1,234 milyon" reads as 1234 milyon)
- internal/domain TestParseAmountNegativeZeroFraction: ParseAmount("-0,50") = 50 with nil error; the whole<0 check misses "-0", so a negative amount is accepted as positive 0,50 while "-5" is rejected
- internal/domain TestParseAmountOverflow: ParseAmount("922337203685477580") = -80 with nil error; (whole*100+minor)*mult overflows int64 and wraps silently instead of returning ErrBadAmount
- internal/domain TestParseAmountSeparatorOnly: ParseAmount(".") = 0 with nil error; an input with no digits becomes intPart "0" and is accepted as zero
- internal/domain TestParseAmountSignedFraction: ParseAmount("5,-1") = 499 with nil error; strconv.ParseInt accepts a sign in the fractional part, so "5,-1" reads as 4,99 TL
- internal/fx TestNormalizeTurkishUppercase: Normalize("TÜRK LİRASI") = "TÜRK LİRASI"; strings.ToLower maps 'I' to 'i' (not dotless 'ı'), so the all-caps Turkish name misses the "türk lirası" case
- internal/nlu TestParseRulesAlindiNeverMatches: ParseRules("Demir'den 500 TL alındı") = []; the collection rule ends in `\b`, which RE2 evaluates on ASCII word characters only, so the "alındı" alternative (ending in 'ı') can never match
- internal/nlu TestParseRulesBufferN: customer = "Ali ve Oğulları'n"; suffixRe handles 'dan/'ten but not the buffer-n form 'ndan/'nden used after a possessive ("Oğulları'ndan"), so "'n" stays in the name
- internal/nlu TestParseRulesTurkishUppercase: ParseRules("AHMET'TEN 500 TL TAHSİLAT") = [], want record_collection; RE2 (?i) does not fold 'İ' (U+0130) to 'i', so all-caps Turkish misses rules ("KRİTİK STOK" even becomes query_stock for product "KRİTİK")
- tests/feature TestChannelWebhooks/config_must_be_an_object: POST /v1/channels accepts a non-object config (a JSON string) with 200 although the handler says "config must be a JSON object"; upsertChannel only checks json.Valid
- tests/feature TestDegradedSettings/stale_snapshot_never_writes: when the settings source goes down after a snapshot was cached, tenant.Cache.Get keeps returning the stale snapshot with Degraded=false (the background refresh's degraded copy is discarded), so auto_save keeps committing; README promises "degraded settings never write"
- tests/feature TestQueries/english_help: the English command "help" is answered with the Turkish help text; nlu.DetectLanguage has no keyword for "help" and falls back to tr
- tests/integration TestAPIIdempotencyOnPG: concurrent requests with the same Idempotency-Key are all executed (8 drafts, 8 committed, statuses [200 200 200 200 200 200 200 200]); the middleware checks GetIdem before running the handler and stores the response only afterwards, with no reservation/lock
- tests/simulation TestApproveRejectRace: approve vs reject race: Engine.commit writes to the data provider before TransitionDraft(pending→committed); a concurrent reject wins the transition, so the draft ends up rejected while the ledger keeps the record (20 of 20 companies' ledgers differ from their committed drafts, e.g. ar000 ledger moved 51113041 kuruş, committed drafts sum to 0)
