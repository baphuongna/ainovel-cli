# Đánh giá hệ thống viết truyện ainovel-cli — note ngày 2026-09-27

> Người viết: session review (local-fix2-vi). Cơ sở: 3 vòng review toàn diện + audit bảo mật + dịch toàn bộ codebase sang tiếng Việt.

## 1. Kiến trúc tổng thể: "Phòng biên tập nhiều vai"

```
Người dùng (interview bootstrap)
   │
   ▼
ARCHITECT ── audit_foundation ──► premise (14 mục) → nhân vật → dàn ý theo tier
   │                                    │ template_ready = false? → bắt làm lại
   ▼
WRITER ──► plan_chapter → draft_chapter → check_consistency → commit_chapter
   │                                                     │
   ▼                                                     ▼
EDITOR (gọt/refactor) ◄── stylestat + review ◄──── guard cơ học
   │
   ▼
ARBITER (chỉ vào khi writer kẹt/lặp lỗi → can thiệp)
```

**Nguyên tắc thiết kế lõi**: LLM **không được tin** — mọi sản phẩm bắt buộc qua tool lưu xuống đĩa (`save_foundation`, `draft_chapter`, `commit_chapter`...). "Chỉ xuất ra khung chat không được tính là hoàn thành."

## 2. Điểm mạnh

1. **Bộ nhớ 3 tầng** (điểm mạnh nhất):
   - `working_memory`: nhiệm vụ hiện tại, `chapter_contract` (beats bắt buộc, cấm kỵ, hook), `previous_tail`
   - `episodic_memory`: tóm tắt chương gần, vị trí truyện, snapshot nhân vật, **phục bút có ID**
   - `reference_pack`: thiết lập thế giới, hồ sơ nhân vật
   - Nén phân cấp: tóm tắt chương → tập (volume) → cung (arc) + `ctxpack` compaction theo ngân sách token → viết 500 chương không phình context.
2. **QC cơ học khi commit**: cấm ngữ/sáo từ AI + fatigue words theo ngưỡng (zh + vi), tiêu đề khớp chính văn, số từ.
3. **stylestat**: số liệu văn phong định lượng (tic AI, tỷ lệ mở màn theo thời điểm, đơn điệu tiêu đề) → editor có bằng chứng, không phán cảm tính.
4. **Editor phân 2 mức**: vá (`edit_chapter`, patch `old_string` chính xác) vs viết lại cả chương.
5. **Arbiter chỉ kích hoạt khi cần** — tiết kiệm chi phí.
6. **advance_gate**: không cho nhảy pha khi nền móng chưa pass audit.
7. **Budget tracker**: cảnh báo %, chặn cứng khi vượt, phát hiện vùng mù giá model.
8. Resume được sau crash (checkpoints + torn-tail healing).

## 3. ĐIỂM YẾU (note chính)

| # | Điểm yếu | Mức độ | Chi tiết |
|---|---|---|---|
| Y1 | **Chất lượng cuối = chất lượng model nền** | Cấu trúc | Hệ thống chỉ orchestration; model kém thì prompt hay mấy cũng ra văn dở. Không có magic prompt. |
| Y2 | **QC regex chỉ bắt tic bề mặt** | Trung bình | Bắt được "khẽ nhíu mày" lặp, nhưng KHÔNG đánh giá được nhịp kể, cảm xúc, twist hợp lý — tầng phê bình văn học vẫn trọn gói cho LLM (editor/arbiter). |
| Y3 | Tách câu theo dấu câu | Nhẹ | sentenceSplit dựa `.!?。！？` — đối thoại lồng/dấu câu phức lệch thống kê stylestat. |
| Y4 | Model quên protocol tool | Trung bình | Đôi lúc phải guard nhắc "phải commit qua tool" → tốn turn (≡ tốn tiền), vòng lặp hành vi. |
| Y5 | Can thiệp giữa chừng headless | Nhẹ | Có intervention + resume nhưng chạy dài tự động khó chỉnh hướng tinh. |
| Y6 | Chi tiết cũ bị nén mất | Tradeoff | Tóm tắt phân cấp = mất chi tiết vặt chương xa; phục bút chỉ sống nếu được chapterfacts bắt kịp. |
| Y7 | Mỗi book độc lập | Nhẹ | Không có memory/kinh nghiệm xuyên sách, xuyên dự án. |
| Y8 | Kém prompt-cache thân thiện | Nhẹ | Prompt dựng động theo chương → cache hit thấp, chi phí cao hơn lý thuyết (có docs/prompt-cache-design.md — debt). |

## 4. Chấm điểm

- 🏗️ Kiến trúc pipeline: **9/10** — tách vai rõ, mọi thứ xuống đĩa, audit được
- 🧠 Bộ nhớ/continuity: **8/10** — 3 tầng + nén phân cấp + phục bút ID
- ✍️ Chất lượng văn chương: **6.5/10** — giới hạn bởi model nền; hệ thống chỉ "không cho hỏng thêm"
- 🇻🇳 Trải nghiệm tiếng Việt (sau fix 2026-09-27): **8.5/10**

**Một câu**: đây là *hệ thống quản lý sản xuất tiểu thuyết* được engineering tử tế, không phải magic — biến một LLM dễ quên, hay sáo thành đội ngũ có kỷ luật: định hướng trước, viết có hợp đồng chương, nhớ được truyện dài, không được tự coi xong khi chưa lưu đĩa.

## 5. Mổ xẻ vòng Editor–Arbiter

### 5.0. Bối cảnh: đây là kiến trúc thế hệ 2 (docs/engine-arbiter.md)

Thế hệ 1 = "một prompt, một LLM cư trú dài hạn điều phối cả sách" (Coordinator) — đẻ ra 7 lớp vá: StopGuard, giao thức lặp lệnh "lần thứ N", quy tắc hành vi coordinator.md, completePhaseGate, MaxTurns=100.000, FlowBoundaryHook, đặc cách kết thúc. 90% call Coordinator chỉ forward lại `flow.Route`. Thế hệ 2 xóa Coordinator (net −1500 dòng): **một Engine tuần tự xác định + worker tự chủ + Arbiter theo nhu cầu + tầng sự thật filesystem**.

### 5.1. Hai mặt phẳng đối xứng (nguyên tắc lõi)

```
Xác định:  flow.LoadState → flow.Route → Instruction      (router.go — test穷举 exhaustive)
Ngữ nghĩa:  arbiter.Collect* → arbiter.Decide* → XxxDecision (decisions.jsonl — replay offline)
            └─ IO thu thập ─┘   └─ quyết định thuần ─┘      └─ Engine thực thi ─┘
```

Phân công: **Route** tra bảng mọi bước tiếp theo tra được; **Arbiter** chỉ xử phán đoán ngữ nghĩa có biên rõ; **Worker** sáng tác mở; **Engine** thực thi quyết định, không tham gia phán văn học.

### 5.2. Vòng EDITOR — 4 đường kích hoạt (flow/router.go, state.go)

1. **Định kỳ**: mỗi 5 chương (`ReviewInterval=5`, `ShouldReview`) → AggregateGlobalReview → Route giao editor scope=global (đọc nguyên văn 3–5 chương gần nhất)
2. **Truyện dài phân tầng**: cuối cung/cuối tập (`ShouldArcReview`) → editor review cấp cung + `save_arc_summary`/`save_volume_summary`
3. **User can thiệp**: intervention arbiter dispatch editor — **kênh DUY NHẤT** đưa chương vào diện viết lại ("tuyệt đối không giao thẳng writer sửa chương đã xong")
4. **writer_feedback nghiêm trọng** (`RequiresImmediateReview`) → giao *planner* (không phải editor) → `revise_outline`/`resolve_outline_feedback`

**Quy trình editor** (editor.md): `novel_context` → bắt buộc `read_chapter` nguyên văn → chấm **7 chiều 0–100** (nhất quán thiết lập / nhân thiết / nhịp điệu / liên tục / phục bút >5 chương / móc câu / thẩm mỹ — mỗi mục con bắt trích dẫn nguyên văn, đối chiếu `style_stats` làm bằng chứng định lượng) → `save_review`.

**save_review = điểm ánh xạ duy nhất verdict→FlowState** (save_review.go:279): `accept→writing`, `polish→polishing`, `rewrite→rewriting`; cập nhật Progress **nguyên tử**; accept cấm chứa issue `requires_change=true`, polish/rewrite phải ≥1 → chương vào `PendingRewrites`.

**Router xử PendingRewrites trước mọi việc viết mới**: giao WRITER "Viết lại chương N"; nếu chưa có chapter_contract → bắt `plan_chapter` trước (editor từng xếp `architect_directive_unclear` là nghiêm trọng nhất — "chỉ nói 'viết lại chương N' chẳng khác nào không cho hướng đi").

**Công cụ sửa**: `edit_chapter` = patch `old_string` chính xác lấy từ read gần nhất (cấm dựng lại từ trí nhớ; khớp duy nhất hoặc replace_all; cấm dùng cho nháp đầu) vs ghi đè cả chương = `draft_chapter(mode="write")`.

### 5.3. Vòng ARBITER — 2 tai

**Tai ngoài — Intervention Arbiter** (arbiter-intervention.md + arbiter/intervention.go):
- Facts snapshot: phase, completed/outlined, pending_rewrites, advance_hold, checkpoint_seq + **5 quyết định gần nhất** (trí nhớ "lần trước sửa tới đâu")
- Decision: `answer → rules → hold → reopen → dispatch` — thứ tự cố định, tối đa 1 dispatch
- Phân luồng: "Viết thế nào"→rules · "Viết cái gì"→architect · "Sửa cái đã viết"→editor queue
- **Chống tràn quyền**: "ngữ cảnh không đồng nghĩa ủy quyền sửa đổi", "phạm vi phân tích không đồng nghĩa phạm vi sửa đổi", "phạm vi tối thiểu đủ dùng"
- Validate cơ học theo facts: reopen chỉ khi phase=complete; hold chỉ phase=writing, target ≥ next chapter; cấm dispatch khi complete
- **Stale-check biên Engine**: DispatchExpect (CheckpointSeq/Phase/Flow/QueueHead) — không khớp → `decision_stale`, hỏi lại bằng facts mới. Can thiệp được tham vấn song song khi worker chạy (read-only), nhưng hold/reopen/dispatch xếp hàng chờ biên Engine tuần tự

**Tai trong — Fault Arbiter** (arbiter-failure.md + engine.go:508):
- **worker_failure**: fail lần 1 → tự retry 1 lần (Route dẫn động bằng dữ kiện → idempotent); fail lần 2 cùng lệnh → DecideFailure: `retry | reroute+dispatch | abort`
- **deadlock** (engine.go:429,74-75): cùng `Agent+Task` lặp liên tiếp → đếm `repeats`; **<3 im lặng · ≥3 tham vấn arbiter (retry KHÔNG reset bộ đếm) · ≥5 ngắt mạch cứng** → pauseStuck. Lỗi không vào nổi worker (không ngữ nghĩa) → giảm bộ đếm (`discardNonSemanticDeadlockAttempt`)
- Arbiter không khả dụng → pauseWithNotify — **không bao giờ tham vấn vô hạn**
- `abort` → pauseStuck + **dropStuckRewrite**: chương kẹt RỜI KHỎI hàng đợi (giữ bản cuối), truyện vẫn tiến tiếp — không đánh đổi cả sách cho một chương
- Whitelist đích giao việc ở mức kiểu: `architect_long/architect_short/writer/editor`
- Model: arbiter cố ý dùng chung Default, bọc `usageTrackedModel` → chi phí vào cùng ngân sách; arbiter chỉ Generate (không stream)

### 5.4. Điểm đắt giá về kỹ thuật

1. **"Trạng thái非法 không thể diễn đạt"** — Decision type riêng từng kịch bản + ValidateAgainst theo facts: LLM không thể phát biểu hành động ngoài kịch bản ngay ở cấp hệ thống kiểu
2. **Mọi output LLM đều không đáng tin** — kiểm tra sự kiện là cửa cuối; decisions.jsonl giữ nguyên `input` để replay offline
3. **Circuit breaker 2 tầng** (3 tham vấn / 5 cứng) + retry-không-reset — chống vòng lặp arbiter↔worker đốt tiền
4. **Stale-check biên + Engine tuần tự** — can thiệp song song mà không race
5. **Ranh giới ủy quyền của editor** — chống LLM "nhiệt tình" tự mở rộng phạm vi viết lại cả sách
6. **pauseStuck thả chương khỏi queue** — graceful degradation: tệ nhất mất 1 chương, không mất cuốn sách
7. Thứ tự biên: cắt lỗ ngân sách xử lý TRƯỚC tạm dừng nghiệm thu (engine.go:273)

### 5.5. Điểm yếu riêng của vòng này (bổ sung bảng §3)

| # | Điểm yếu | Chi tiết |
|---|---|---|
| E1 | **Nhịp review cố định** | Global review mỗi 5 chương cứng (`ReviewInterval=5`) — không theo tín hiệu chất lượng (stylestat xấu không tự trigger review) → có thể review thừa chương tốt / bỏ sót chương lỗi |
| E2 | **Verdict do editor tự quyết** | 7 chiều chấm điểm nhưng code không ép quan hệ điểm→verdict; editor dễ dãi/khó tính đều không audit được bằng số (đồng họng với Y2) |
| E3 | **Arbiter dùng chung model Default** | Model chính yếu thì phán định retry/reroute cũng yếu — không có role model riêng (chủ ý theo doc, nhưng là debt thật) |
| E4 | **Token review lớn** | Global review bắt đọc nguyên văn 3–5 chương + novel_context đầy đủ → đắt theo chu kỳ 5 chương, không có sampling |
| E5 | Deadlock counter theo (Agent+Task) | Ổn cho task Route sinh (deterministic); task từ intervention do arbiter tự viết — nếu mỗi lần khác chữ thì key khác → không tích lũy; nhưng intervention không tự lặp nên rủi ro thấp |
| E6 | Docs engine-arbiter.md giữ tiếng Trung | Người Việt đọc thiết kế này khó (giữ theo Option A dịch) |

(Ghi chú: `completion_dispute` không dựng là quyết định có chủ ý — không tính điểm yếu.)

---

*Nghiên cứu bằng: đọc trực tiếp 4 prompt (editor/intervention/failure/revision-analyze) + arbiter/{arbiter,intervention,failure}.go + save_review/edit_chapter + flow/{router,state}.go + engine.go + arbiter_model.go + docs/engine-arbiter.md. Explorer subagent chết sớm (WSL) — làm tay toàn bộ.*

## 6. Mổ xẻ vòng Revision-Sync (user sửa chương tay) + Advance Gate/Hold

### 6.1. Revision-Sync: phát hiện user sửa tay bằng SHA256 (internal/revision/)

```
User sửa chapters/NN.md bằng tay
  → revision.Scan: SHA256(file hiện tại) ≠ ChapterRecord.ContentSHA256  (scan.go:22)
  → Host.SyncChapterRevisions (host.go:1782):
       chiếm slot độc chiếm acquireExclusive (engine phải đang nghỉ)
       + budget.Refuse() (hết ngân sách thì từ chối ngay, không gọi LLM)
  → Service.Sync (service.go:39):
       [2-phase crash-safe] lưu PendingRevision TRƯỚC mỗi bước biến đổi
       duyệt thay đổi NGƯỢC thứ tự (chương mới nhất trước) — proposedSummaries
       cascade: phân tích chương cũ thấy facts mới của chương mới hơn
       → Analyze (LLM revision-analyze.md): Facts đầy đủ (không chỉ diff),
         StyleDelta (chỉ ghi "sở thích tái sử dụng" — lỗi chính tả/kể tên
         không tính), StoryChanged, OutlineImpact, DownstreamIssues
       → validate cơ học (OutlineImpact phải có Deviation+Suggestion...)
  → applyPending 3 giai đoạn (service.go:112):
       prepared → ghi ChapterRecord mới (Revision+1, Origin=user)
       records_applied → Projector.Apply: DỰNG LẠI toàn trạng thái phái sinh
         từ records (facts là sự thật, projection tính lại)
         + InvalidateChapterAggregates (tổng hợp cung/tập sau chương sửa → vô hiệu,
           Router sẽ tự giao editor làm lại qua AggregateRefresh)
         + nếu chương đang trong PendingRewrites → CompleteRewrite (sửa tay = xong việc viết lại!)
       → StoryChanged/OutlineImpact/DownstreamIssues → AppendOutlineFeedback
         → ImmediateFeedbackCount++ → Router giao planner revise_outline (VÒNG KHÉP KÍN)
       projections_applied → checkpoint từng chương + styleIndex cập nhật
```

**Tường nhất quán**: `requireCleanChapters` (host.go:1823) — mọi luồng khác từ chối chạy khi chương đang bẩn: *"Phát hiện phần thân chương đã bị sửa từ bên ngoài; vui lòng chạy /sync trước"*. Không có đường nào đọc trạng thái nửa vời.

**An toàn sự cố**: PendingRevision có stage → crash giữa chừng thì resume tiếp từ stage đã lưu, KHÔNG phân tích lại LLM; baseline cũ không khớp nữa (errPreparedStale) → xóa pending, làm lại từ đầu.

### 6.2. Advance Gate + Hold: chính sách tiến trình (advance_gate.go + flow/advance.go)

**Hai chế độ tiến trình**: `auto` (tự do chạy) vs **nghiệm thu từng chương** (permit). Nguyên tắc: *"Gate không tham gia Route, không diễn giải Task/Reason, không phán đoán văn học"* — thành phần chính sách thuần.

- `Allow(inst)` trước khi phát worker: chỉ thị nào **bắt đầu chương tiến-đứt mới** (`StartsForwardChapter`: writer + phase=writing + không PendingCommit + không PendingRewrites + không InProgressChapter + target==NextChapter) mới cần giấy phép; **viết lại/trau chuốt không cần** — kiểm soát chỉ chặn tiến mới, không chặn sửa cũ
- Chế độ nghiệm thu: permit sai chương → lỗi rõ ràng; permit chương đã xong nhưng **thiếu commit checkpoint** → phát hiện trạng thái rách ("đánh dấu hoàn thành nhưng thiếu commit checkpoint") — fail loud, không âm thầm chạy tiếp
- Bất biến **một giấy phép một chương**: chế độ auto còn sót permit → lỗi (không cho trạng thái lai)
- `HandleBoundary()` sau mỗi vòng Engine (thứ tự: **budget boundary TRƯỚC gate** — cắt lỗ ưu tiên)

**Hold (tạm dừng một lần) — ResolveAdvanceHold hàm thuần** (flow/advance.go:35):

| After | Giữ hold khi... | Tiêu thụ & dừng khi... |
|---|---|---|
| `boundary` | — | chạm biên làm việc hiện tại |
| `rewrites_drained` | PendingRewrites còn | hàng viết lại xả hết |
| `chapter N` | LatestCompleted < N | đã hoàn N **và** đối chiếu ổn định: PendingCommit rỗng + N nằm trong CompletedChapters + commit checkpoint tồn tại (targetChapterCommitted) |
| sách đã hoàn thành (phase=complete) | — | hold tự giải trừ (ý định vô nghĩa vì sách xong) |

Mọi trạng thái lạ → **lỗi tường minh, không âm thầm hạ cấp theo "tiếp tục chạy"**. Hold chỉ được đặt bởi intervention arbiter (đã validate ở tầng type: chỉ phase=writing, target ≥ next).

### 6.3. Điểm yếu vòng này (bổ sung)

| # | Điểm yếu | Chi tiết |
|---|---|---|
| E7 | Scan yêu cầu ChapterRecord cho MỌI chương đã hoàn thành | Dự án cũ thiếu record → hard-fail "không thể nhận diện an toàn sửa đổi từ bên ngoài" — an toàn nhưng không khoan dung, không có đường migrate lỏng |
| E8 | Phân tích sửa đổi = N lần gọi LLM + Projector rebuild toàn bộ | Sửa tay nhiều chương một lúc thì tốn N call + dựng lại projection toàn tập; không có cap/batch (cost) |
| E9 | StyleDelta do LLM phán "sở thích tái sử dụng" | Ranh giới "sửa tên riêng" vs "sở thích văn phong" mơ hồ — cùng họng Y2/E2: phán ngữ nghĩa cuối vẫn trọn gói niềm tin |
| E10 | Nghiệm thu từng chương ở chế độ permit là thủ công thuần | Không có chế độ "auto tới N chương rồi nghiệm thu"; người dùng phải gõ /next mỗi chương — hợp tác dày (đổi lấy kiểm soát) |

---
*Nghiên cứu §6: đọc revision/{scan,service,analyze,projector}.go + host.go wrapper (1770–1830) + advance_gate.go (toàn bộ) + flow/advance.go (toàn bộ).*

## 7. Mổ xẻ 3 subsystem phụ: Import / Cocreate / UserRules

### 7.1. Import pipeline (internal/host/imp/) — "trình biên dịch ngữ nghĩa phân giai đoạn"

> *"Mô hình phụ trách hiểu ngữ nghĩa mở; mã nguồn phụ trách tọa độ, độ phủ, kiểu, băm, thứ tự và tính lũy; toàn bộ sản phẩm ngữ nghĩa phải xác thực trong workspace độc lập xong mới phát hành vào trạng thái sách chính thức."* (docs/import-pipeline.md, 973 dòng)

```
văn bản ngoài → đọc & chuẩn hóa tất định (Go)
  → LLM nhận diện biên chương/tập/phụ lục
  → GO CHỨNG MINH ĐỦ PHỦ TOÀN VĂN (mỗi đoạn không trống phải thuộc đúng 1 chỗ)
  → user xác nhận (hoặc --yes tường minh)
  → LLM trích sự thật từng chương theo batch liên tục (kép ngân sách input/output)
  → LLM tổng hợp phân tầng (range-digest → BookSynthesis)
  → Go lắp Foundation + xác thực
  → phát hành lũy: Foundation + từng chương qua commit_chapter (digest idempotent)
  → dừng mặc định; --continue mới nối theo gate bình thường
```

**Tại sao viết lại** (bản cũ 4 lỗi cấu trúc): regex cắt chương không bao giờ穷尽 định dạng tự nhiên; một call ReverseFoundation đọc toàn bộ + xuất toàn bộ JSON (54 chương đã cắt cụt); ghi trạng thái chính thức từng phần → hỏng giữa chừng để lại sách nửa导入; hard-code kết luận ngữ nghĩa (nhất quyết 1–3 cung, ngưỡng 25/80 chọn short/mid/long, cố tình bịa `open_threads` để "tiếp viết được").

**8 bất biến lõi (§16)** đáng chú ý nhất: sản phẩm workspace = `SchemaVersion + InputDigest + Payload` (chỉ tái sử dụng khi digest dựng lại được từ input thật); batch đứt (`StopReasonLength`) chỉ cứu **tiền tố liên tục hợp lệ từ chương đầu**, thiếu một chương là chặn cả chuỗi sau; `done` phải được workspace + artifact chính thức + Progress + PendingCommit + checkpoint **cùng chứng minh** — engine không được старт trước `done`; thất bại model không bao giờ bị diễn giải thành "không có nội dung" hay "bỏ qua chương này".

Runner = mọi bước `LoadState → NextAction → thực hiện 1 action` (Ingest/Segment/AwaitConfirmation/Analyze/Synthesize/Publish/Done/AwaitStoryResolution) — không phải agent tự do, là biên dịch có bước. 3 role model riêng (`import_segment/import_analyze/import_synthesize`); `--guide` cho user hướng dẫn cắt bằng ngôn ngữ tự nhiên; response gốc lưu `failures/`.

### 7.2. Cocreate — phỏng vấn ý định (2 chế độ, internal/host/cocreate.go)

- **Khởi đầu**: nhiều vòng hội thoại ngắn → chắp dần `<draft>` chỉ lệnh sáng tác → Ctrl+S giao engine
- **Theo giai đoạn** (đang viết dở, tạm dừng bàn hướng đi): "Luật sắt" — *mọi đề xuất phải nhất quán cốt truyện/nhân vật/phục bút đã xảy ra, tuyệt đối không lật đổ nội dung đã viết, chỉ lập kế hoạch đoạn sau*. Ngữ cảnh: tiến độ, story_compass (hướng kết + tuyến đang mở), tóm tắt quyển gần nhất, ≤8 nhân vật chính, vị trí tập/cung

**Chi tiết kỹ thuật đắt**: protocol 4 thẻ XML `<reply>/<draft>/<ready>/<suggestions>` — comment giải thích tại sao XML thắng marker ngoặc vuông: *dữ liệu huấn luyện tràn ngập `<thinking>...` nên model gần như không bao giờ hỏng thẻ; thẻ đóng cho phép cắt streaming chính xác giữa dòng*. `<draft>` **phải viết lại nguyên vẹn mỗi vòng** — cấm "(giữ như vòng trước)": bản nháp không-trạng-thái → resume được, không có ngữ cảnh ẩn trôi. `<suggestions>` viết bằng **giọng người dùng**, ≤25 chữ, "đưa xu hướng chứ không thay người dùng thiết lập trọn". Cocreate chạy trong cửa sổ độc chiếm (engine nghỉ).

### 7.3. UserRules — một cuốn sách một snapshot quy tắc (docs/user-rules-runtime.md + internal/userrules + internal/rules)

```
startup prompt + file .ainovel/rules/*.md + cập nhật runtime (intervention arbiter `rules`)
  → LLM chuẩn hóa TỪNG NGUỒN (hợp đồng typed: genre/forbidden_chars/forbidden_phrases/fatigue_words[])
  → GO HỢP NHẤT TẤT ĐỊNH theo ưu tiên:
      system_defaults → global rules → project rules → startup prompt → runtime update
  → meta/user_rules.json (snapshot duy nhất)
  → novel_context tiêm một nơi → architect/writer/editor/commit check dùng chung
```

- **Ưu tiên không giao cho LLM** — Go làm field-override; `preferences` không đè nhau mà **nối theo thứ tự ưu tiên** (nguồn cao đứng sau), mâu thuẫn mềm do LLM cân theo thứ tự
- **Chính sách hỏng hạt mịn**: một trường xấu → chỉ trường đó bị vứt, nguồn khác giữ nguyên; cả lần chuẩn hóa hỏng (mạng/JSON) mới hạ nguồn xuống raw preferences + `status=degraded`; lỗi sửa được → tự chữa kèm lý do chính xác
- Baseline cơ học `SystemDefaults()` sống **trong code** (đã migrate khỏi assets/rules/default.md, xóa yaml.v3) — nay chứa cả bộ vi (fix 2026-09-27)
- Kênh rules runtime = intervention arbiter (`answer → rules → hold → reopen → dispatch`): "Viết thế nào" → rules (hiệu lực từ chương sau, không hồi tố)

### 7.4. Điểm yếu vòng này (bổ sung)

| # | Điểm yếu | Chi tiết |
|---|---|---|
| E11 | Import phân tích tuyến tính theo chương | Sách dài 100+ chương = nhiều batch call + range-digest nhiều tầng — đắt và chậm; chỉ giảm rủi ro chứ không giảm số call |
| E12 | `preferences` nối không giải mâu thuẫn | Chạy dài user đưa sở thích mềm trái chiều → cả hai cùng nằm trong snapshot, LLM tự cân — có chủ ý nhưng dễ đùn trách nhiệm |
| E13 | Cocreate viết lại `<draft>` nguyên vẹn mỗi vòng | Đốt token theo số vòng — tradeoff có chủ ý cho tính không-trạng-thái |
| E14 | ~~Import: chế độ JSON Schema native chưa làm~~ **ĐÃ PHẢN BÁC trong verify vòng 2 (xem §10.4)** — imp/call.go dùng llmcontract.Execute (native khi model hỗ trợ); chỉ ghi chú TODO ở đầu doc là cũ | ~~Doc §13.2 mức 1 đánh dấu TODO~~ |
| E15 | UserRules chuỗi đầy đủ với LLM thật "chưa nghiệm" | Doc ghi rõ "未验": normalizer offline 10/10 nhưng end-to-end mở sách thật + arbiter rules chưa chạy thử toàn tuyến |

---
*Nghiên cứu §7: docs/import-pipeline.md (973d, §1-6/§16-17) + imp/ (18 file, runner actions) + cocreate.go/cocreate_stage.go (protocol + stage context) + docs/user-rules-runtime.md (337d) + rules/snapshot.go + userrules/normalize.go.*

## 8. Mổ xẻ tầng sử dụng model

### 8.1. Phân vai model (bootstrap/models.go + config.go)

**ModelSet**: 6 role cấu hình được — `architect / writer / editor` (3 vai sáng tác) + `import_segment / import_analyze / import_synthesize` (núm giá cho hàm ngữ nghĩa nhập: "hàm cơ giới hơn có thể trỏ sang model rẻ hơn", doc import §13.1). Role chưa cấu hình → **Default**. `ReasoningEffort` (off/low/medium/high/xhigh/max) cấu hình cấp cao nhất + phủ theo role.

- **Arbiter cố ý KHÔNG có role riêng** — luôn Default (doc engine-arbiter: "chỉ mở khi có nhu cầu năng lực/chi phí rõ")
- Runtime swap: `/model` đổi nóng (`SwappableModel` + RWMutex); `/config` quản provider/model — **không cho xóa model đang được role tham chiếu** ("vui lòng chuyển ở /model trước")
- Swap xong tự phát lại mức thinking cho từng role (`applyThinkingLocked`)

### 8.2. Failover cấp-một-request (models.go:399-555)

- Chỉ kích hoạt khi role có chuỗi `fallbacks` tường minh trong config; không có → suy biến thành model thường
- `Generate`: lỗi → `pickFallback` → thử **đúng 1 fallback** (không đệ quy chuỗi) — chống retry storm; `GenerateStream`: retry 1 lần sang fallback giữa stream rồi forward tiếp
- Điều kiện loại: `context.Canceled` bỏ qua; chỉ lỗi hạ tầng eligible (`agentcore.IsFailoverEligible`); request cần JSON Schema → fallback phải hỗ trợ; skip target trùng primary; mỗi lần chuyển phát `FailoverEvent` WARN (from/to/reason/err)

### 8.3. Hợp đồng structured output (internal/llmcontract)

Mọi điểm LLM-as-function (3 kịch bản arbiter, userrules normalize, import, revision-analyze) cùng một cơ chế: **một Contract = một JSON Schema**; `Execute` chọn mode theo năng lực model — native JSON Schema nếu khai báo hỗ trợ, không thì **cùng schema đó** nhét vào prompt. Lỗi schema (prompt-mode) + lỗi nghiệp vụ (cả 2 mode) → **tự chữa kèm lý do chính xác** (`promptCorrection` / `semanticCorrection`); native contract violation /拒答 / cắt cụt → lộ ngay không chữa. Arbiter dùng `decideMaxTokens=8192` — phần lớn dành ngân sách thinking cho model Reasoning.

### 8.4. Giá & ngân sách (internal/models + host/usage.go)

- Bảng giá nạp từ **openrouter models API** + cache đĩa + refresh nền (`StartPricingRefresh`/`MergeModels`); lọc model cũ (`isStaleModel`); chuẩn hóa tiền tố vendor (`providerMap`)
- `usageTrackedModel` bọc MỌI đường gọi (worker, arbiter, revision, import) → `UsageTracker.Record` → `meta/usage.json` (ghi debounce, cửa sổ trượt N mẫu gần nhất theo role)
- Budget: cảnh báo theo % → chặn cứng; **phát hiện vùng mù giá** — ghi sổ liên tục mà tổng đứng yên = model không có giá trong registry → cảnh báo "hạn mức sẽ không kích hoạt"
- Theo dõi chuỗi cache per-role (cacheRead/input) → phát hiện đứt gãy cache

### 8.5. Prompt cache 3 tầng (docs/prompt-cache-design.md — tài liệu dạy học có case thật)

Case gốc: **sách 33 chương tốn $58, cache hit chỉ 8.5%** (coordinator 2.7%, architect 0). Mổ usage.json ra 3 nguyên nhân: (1) **tools bytes rung** — Go map iteration ngẫu nhiên → schema tool đổi thứ tự mỗi vòng → prefix hỏng từ byte 0; (2) **không route affinity** — OpenAI không nhận `prompt_cache_key`, 33 session byte-đồng-nhất chỉ hit 12; (3) Claude không đánh `cache_control` = cache bằng 0.

Giải pháp 3 cấp cache identity (build.go `promptCacheBase`): `nvl-<hash(bookDir)>-<role>#<spawnSeq>` — **một sách một base** (hash, không lộ path local) + một role một tên + một spawn một key. Claude: `CacheLastMessage:"ephemeral"` (điểm lăn). 

**"闩锁 đỏ" — nguyên tắc đơn điệu session**: mọi thứ vào prefix (system prompt/tools/thinking/sampling) **đóng băng sau lần tính đầu** trong session — *"thà cũ, chứ đỡ vỡ cache"*; đổi thinking chỉ hiệu lực cho spawn mới. Đây là lý do `applyThinking` không đụng session đang chạy.

### 8.6. Điểm yếu tầng model (bổ sung)

| # | Điểm yếu | Chi tiết |
|---|---|---|
| E16 | Failover chỉ 1 bậc/request | Không leo qua cả chuỗi fallback — chủ ý tránh storm, nhưng primary + fallback cùng chết là dừng luôn (hy sinh dự phòng sâu) |
| E17 | **Cocreate gọi `ForRole("thinking")` — role không tồn tại trong knownRoles** | Luôn rơi về Default; user muốn model riêng cho phỏng vấn cocreate không có đường cấu hình (phải đổi Default toàn cục). UserRole validation sẽ từ chối "thinking" nếu user thử khai báo — bất nhất |
| E18 | Giá phụ thuộc registry openrouter | Model tự host / quá mới → không có giá → budget mù (đã cảnh báo nhưng vẫn mù); giá vi không tách theo market |
| E19 | Arbiter không tách model (đồng họng E3) | Control-plane phán định dùng chính Default có thể là nguồn sự cố — phán "retry/reroute" cũng do model cùng họ |

---
*Nghiên cứu §8: bootstrap/{models,config}.go (ModelSet/SwappableModel/failoverModel/knownRoles) + host/{model_config,usage}.go + llmcontract/{execute,contract}.go + models/{pricing,registry}.go + docs/prompt-cache-design.md (12 mục) + cocreate.go:92.*

## 9. Mổ xẻ trình quản lý model (catalog + registry + setup + /model,/config)

### 9.1. Catalog 3 tầng dữ liệu (internal/models)

1. **Baseline compile-time** (`models_generated.go`, sinh bởi `go generate` từ openrouter API — gen_models.go): **212 model / 12 provider** (openai 51, qwen 45, gemini 26, mistral 21, anthropic 14, glm 12, deepseek 12, meta-llama 10, minimax 7, moonshot 5, mimo 5, grok 4), đóng băng ngày 2026-05-31. `ModelEntry` = provider + id + name + context_window + max_tokens + **4 giá** (input/output/cacheRead/cacheWrite).
2. **Cache đĩa** + **refresh nền** (`StartPricingRefresh` goroutine): cache miss → fetch openrouter → ghi cache → merge.
3. **`MergeModels` hợp nhất thận trọng**: chỉ đè giá khi fetched **>0** (openrouter thiếu giá không đè baseline về 0 — giữ 0 cũ không tệ hơn 0 mới); window/maxtokens/name tương tự; model lạ append thêm.

### 9.2. Resolve mờ (model_lookup.go + registry.Resolve)

Thứ tự: `"provider/model"` exact (không khớp thì bỏ tiền tố vendor tra lại — vì `google/`, `x-ai/` của openrouter không trùng tên provider cục bộ) → exact/date-suffix → substring (ID hoặc Name chứa pattern).
- **Date-suffix match**: `claude-sonnet-4` khớp `claude-sonnet-4-20250514` (hậu tố `-YYYYMMDD` 8 chữ số); normalize `.`→`-`, không hoa thường
- Nhiều match → **ưu tiên bản không kèm hậu tố ngày**
- `SameModelID` (so sánh "có phải cùng model") dùng cho đổi model/cache identity/failover skip trùng

### 9.3. Registry nuôi hệ thống ở đâu

- **`ResolveContextWindow` → context_manager**: cửa sổ ngữ cảnh của ctxpack **theo model đang dùng** — model 128K và 1M có chiến lược nén khác nhau; đây là chỗ catalog chạm trực tiếp vào hành vi sáng tác
- **UsageTracker**: tra giá 4 hạng mục theo (provider, model); không có → lùi về `msg.Usage.Cost.Total` do provider tự báo (có thể 0 → "vùng mù giá" đã cảnh báo ở §8.4)
- **/model UI**: `CandidateModels(provider)` — danh sách model **từ config user**, không phải cả catalog; hiển thị context window + nguồn (registry/config/estimated)

### 9.4. Setup wizard lần đầu (bootstrap/setup.go)

- **11 preset provider**: ollama (local, không cần key), openrouter, gemini, anthropic, deepseek, openai, qwen, glm, grok, bedrock (key tùy chọn), **custom proxy** (bắt chọn chuẩn API: openai-compatible/anthropic)
- Chọn ngôn ngữ trước: `vi` (mặc định, "văn phong Việt tự nhiên") / `zh` (nguyên bản); chọn văn phong (styles); key nhập masked; credential lưu **global** (chính sách V-2.1 sau fix — không rơi vào project layer)

### 9.5. Luồng /config + /model (host/model_config.go, 420 dòng)

- **Draft 2 pha**: TUI dựng `ModelConfigurationDraft` → `prepareProviderDraftLocked` (chuẩn hóa + merge vào **bản sao** config) → commit `saveModelConfigurationLocked` — đường validate thống nhất cho cả lưu lẫn test kết nối
- **Xóa model đang được role tham chiếu → chặn** kèm hướng dẫn ("dùng /model chuyển trước"); **rename model → tự cập nhật tham chiếu** (`validateModelRenames` + `renameModelReferences`)
- **TestModelConnection**: dựng ModelSet thử từ draft (Roles=nil), gọi `Generate("Reply OK.")` **thật** — không phải ping danh tính, là smoke test đầu-cuối thật
- Sau mọi thay đổi: phát lại mức thinking từng role

### 9.6. Điểm yếu trình quản lý model (bổ sung)

| # | Điểm yếu | Chi tiết |
|---|---|---|
| E20 | Baseline đóng băng 2026-05-31 | Model ra sau cần refresh nền thành công (có mạng + openrouter sống) mới có giá; offline với model mới → vùng mù giá im lặng (chỉ cảnh báo budget, không gợi registry thiếu) |
| E21 | Date-suffix chỉ nhận `-YYYYMMDD` | Biến thể `-v2`/`-latest`/`-preview`/`-0613` không match → `SameModelID` false âm — đổi model giữa các biến thể có thể làm vỡ cache identity / failover không nhận ra trùng |
| E22 | Substring match không ranking | Resolve nhiều match chỉ ưu tiên "không dated-suffix"; không sort theo độ dài/tân độ → "gemini-flash" có thể resolve nhầm bản lạ (flash-lite?) — người gọi dùng kết quả đầu không kiểm chứng |
| E23 | Test kết nối tốn 1 call thật | Smoke test bằng tiền thật (rẻ nhưng không free; không có chế độ dry-run chỉ handshake) |
| E17′ | (nhắc lại) cocreate `ForRole("thinking")` | Role không cấu hình được — trình quản lý không hề biết role này tồn tại |

---
*Nghiên cứu §9: models/{registry,model_lookup,models_generated,gen_models,pricing}.go + bootstrap/setup.go (presets, wizard) + host/model_config.go (draft/test/rename) + usage.go (đường tra giá) + context_manager.go (điểm dùng ResolveContextWindow).*

## 10. Verify findings + đánh giá ảnh hưởng fix (2026-09-27, đã áp dụng)

### 10.1. Kết quả verify từng finding

| Finding | Verdict | Bằng chứng |
|---|---|---|
| E17 (role "thinking" mồ côi) | ✅ XÁC NHẬN + **nâng cấp**: còn nghiêm trọng hơn — `coCreateStream` (host/cocreate.go) **không bọc usageTrackedModel và không gọi usage.Record** → toàn bộ chi phí phỏng vấn đồng sáng tác **vô hình** với meta/usage.json và ngân sách (budget.Refuse không thấy). Người dùng phỏng vấn dai dẳng bằng model đắt vẫn chạy qua hạn mức | host.go:1676/1682 truyền `h.models` thẳng; cocreate.go:134 gọi GenerateStream không ghi sổ |
| E21 (SameModelID hẹp) | ⚠️ **PHẢN BÁC NỬA** — `SameModelID` là **dead code** (0 call-site ngoài test) → ảnh hưởng tôi nêu trước đây bị phóng đại. Hơn nữa tính hẹp là **đúng chủ ý**: `-preview`/`-exp`/`-fast` là model KHÁC — coi là cùng model sẽ nguy hiểm. Dạng ngày gạch của OpenAI (`-2024-08-06`) là false âm đã biết, chấp nhận theo hướng thận trọng | grep toàn repo: chỉ định nghĩa + test |
| E22 (substring ranking tùy ý) | ✅ XÁC NHẬN, có **bằng chứng thực nghiệm nghiêm trọng**: `Resolve("claude-opus")` → `claude-opus-4` ($75/1M, cửa sổ 200K) trong khi catalog có 4.5–4.8 ($25/1M, 1M cửa sổ) — **sai giá 3x + sai cửa sổ ngữ cảnh 5x** theo thứ tự catalog; `Resolve("gpt-4o")` → bản dated CŨ NHẤT (2024-08-06) | test thăm dò thực chạy trên NewModelRegistry() |

### 10.2. Fix đã áp dụng (4 thay đổi, đều lockstep-test)

| Fix | File | Nội dung | Ảnh hưởng |
|---|---|---|---|
| A. Ghi sổ usage cocreate | host/cocreate.go + host.go | Thêm callback `record` vào coCreateStream; gọi `record("cocreate","",ev.Message)` tại StreamEventDone; 2 caller truyền `h.usage.Record` | Chi phí phỏng vấn giờ vào usage.json + ngân sách (onCost → BudgetSentinel). Trong suốt với hành vi cũ về mặt chức năng — chỉ thêm kế toán. Role label `cocreate` trong perAgent |
| B. Ranking Resolve | models/model_lookup.go + registry.go | Tiêu chí 4 bậc: prefix > chứa; đuôi thuần số > đuôi chữ; version tự nhiên GIẢM DẦN (mới nhất thắng: 4.8>4.5>4, 2024-11-20>2024-08-06); giữ quy tắc ưu tiên non-dated. Đuôi lấy sau vị trí khớp có ranh giới phân tách (khớp giữa ID như "sonnet" vẫn tính được "-4.5") | Alias ngắn giờ resolve về bản chuẩn MỚI NHẤT: `claude-opus`→opus-4.8 ($25/1M thay $75), `gpt-4o`→2024-11-20, `sonnet`→4.6. **Hành vi đổi** cho ai đang config bằng alias — hướng đúng giá/cửa sổ; ID chính xác không đổi |
| C. Failover skip qua bí danh ngày | bootstrap/models.go | pickFallback so `models.SameModelID(target.name, current.name)` thay so sánh chuỗi — fallback "claude-sonnet-4" khi primary "claude-sonnet-4-20250514" giờ bị skip (cùng model) | Hết浪费 1 request failover vô ích vào chính model đang lỗi; SameModelID từ dead code thành code sống |
| D. Role "thinking" hợp thức | bootstrap/config.go + comment cocreate.go | Thêm `"thinking": true` vào knownRoles + cập nhật câu lỗi validation | User cấu hình được model riêng cho phỏng vấn cocreate (roles.thinking); backward-compatible (không cấu hình → vẫn Default như cũ) |

Test mới: `models/registry_rank_test.go` (TestResolveSubstringRanking — chốt 4 alias + not-found "gemini-flash"; TestSameModelIDMatrix — chốt chủ ý hẹp của suffix), `bootstrap/roles_test.go` (thinking được chấp nhận, role lạ vẫn bị chặn).

Lỗi tự bắt trong lúc fix: phiên bản đầu của ranking chỉ tính đuôi khi ID **prefix**-khớp pattern — "sonnet" nằm giữa ID nên vẫn chọn sai; đã sửa lấy đuôi sau vị trí khớp có ranh giới. Test lockstep bắt đúng lỗi này (verify có giá trị kép).

### 10.3. Tổng ảnh hưởng rủi ro sau fix

- Không đổi protocol/lưu trữ; chỉ kế toán (A), giải mờ (B), tránh request lãng (C), mở khóa cấu hình cộng thêm (D)
- Bộ test toàn repo xanh (build/vet/test/race trên 3 package chạm); binary rebuild `local-fix2-vi`
- Rủi ro còn lại: (B) người dùng bằng alias thấy giá/cửa sổ đổi số — đúng thực tế hơn nhưng cần biết; (D) UI /config chưa có mục "thinking" trong nhãn mô tả (validation + docs là nguồn sự thật)

### 10.4. Verify vòng 2 — các finding cơ học còn lại (E1/E2/E5/E8/E11/E14)

| Finding | Verdict | Bằng chứng (file:dòng) |
|---|---|---|
| E1 — review mỗi 5 chương cứng, stylestat không tự trigger | ✅ XÁC NHẬN | AggregateRefresh chỉ được sinh từ `ShouldReview` (mỗi `ReviewInterval=5`, domain/chapter.go:9) và biên cung/tập (flow/state.go:78,86,95,136). styleStats chỉ được tiêm vào novel_context/commit/revision (host.go:207-216,1814,1859) — **không có đường nào từ stylestat đến Route** |
| E2 — verdict do editor tự quyết, code không ép score→verdict | ✅ XÁC NHẬN | save_review.go: kiểm tra cơ học duy nhất là "accept cấm requires_change" (:257) + "polish/rewrite cần ≥1" (:260) + score chỉ kẹp 0–100 (:309); grep toàn repo không có quan hệ suy verdict từ điểm |
| E5 — deadlock counter theo (Agent+Task) nguyên văn | ✅ XÁC NHẬN | engine.go:607 `instructionKey = Agent + "\x00" + Task` — task Route sinh deterministic (fmt theo state) nên ổn; task intervention do arbiter tự viết → khác chữ là khác key, không tích lũy (rủi ro thấp như đã đánh giá vì intervention không tự lặp) |
| E8 — preferences nối không giải mâu thuẫn | ✅ XÁC NHẬN | rules/snapshot.go:94-100: preferences các nguồn nối bằng header `## [nguồn]`, thuần concat, không hề giải xung đột (E12 trùng nội dung này — gộp) |
| E11 — import analyze tuyến tính theo chương | ✅ XÁC NHẬN | imp/analyze.go duyệt batch 1..N chương (loop :86,104,127...); không song song, không sampling |
| E14 — import chưa có JSON Schema native | ❌ **PHẢN BÁC** | imp/call.go:164 `callStructured` → `llmcontract.Execute` — cùng cơ chế chọn native JSON Schema theo năng lực model như arbiter/userrules. Doc import-pipeline.md §13.2 THÂN BÀI đã mô tả đúng hiện trạng ("四类导入产物共用 llmcontract.Execute...原生 JSON Schema"); chỉ **ghi chú "修订 2026-07-16" ở đầu doc còn câu TODO cũ** — doc header stale, không phải code thiếu |

**Tổng kết verify 2 vòng**: 23 finding E — 20 xác nhận (3 đã fix: E17+usage, E22, và C/D kèm theo), 1 phản bác hoàn toàn (E14 — doc header cũ làm tôi tin nhầm), 2 phản bác nửa/chủ ý-đúng (E21 hẹp là đúng thiết kế; E12 trùng E8). Các finding còn lại là đánh giá thiết kế (Y1-Y8, E3/E4/E6/E9/E10/E15...) — không verify tĩnh thêm được.

### 10.5. Fix theo đề xuất sau verify (2026-09-27, đợt 2)

| Việc | Kết quả | Chi tiết |
|---|---|---|
| **#1 Regenerate catalog** | ✅ | `go generate` từ openrouter: 212 → **223 model**, baseline 2026-09-27 (opus-5/5.5, sonnet-5, fable-5, deepseek-v4...). Ranking mới tự chọn đúng bản chuẩn mới nhất (test kỳ vọng cập nhật lockstep — có comment nhắc soát lại khi regenerate sau) |
| **#2 Giá do user khai** (E18) | ✅ | `ModelConfig` + 4 field giá (`input/output/cache_read/cache_write_per_1m`) + cờ `Manual` trên `ModelEntry`: entry khai báo tay **ghim** — refresh openrouter không đè (chỉ Manual đè được Manual). `Config.ManualPriceEntries()` merge vào `DefaultRegistry()` ngay trước `StartPricingRefresh` (host.go). Validate giá ≥ 0 |
| **Bonus phát hiện khi implement** | ✅ | `context_window` khai trong config trước đây **chỉ hiển thị TUI** — ctxpack đọc registry (build.go:82) nên window khai báo không bao giờ ảnh hưởng nén ngữ cảnh; nay qua đường Manual entry, window config thẳng tới ctxpack. Đã ghi chú trong config.example.jsonc |
| Ví dụ + docs | ✅ | `config.example.jsonc` (cả 2 bản root + internal — có test ép đồng bộ) có ví dụ khai giá dạng comment |
| Test mới | ✅ | `TestMergeModelsManualPin` (4 kịch bản pin/đè/giữ hành vi cũ), `TestManualPriceEntries` + validation giá âm |

Cấu hình mẫu cho model tự host:
```jsonc
"models": [
  { "name": "my-private-model", "context_window": 262144,
    "input_cost_per_1m": 0.5, "output_cost_per_1m": 1.5,
    "cache_read_cost_per_1m": 0.05, "cache_write_cost_per_1m": 0.625 }
]
```
