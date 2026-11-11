# PRD — multica-intake

Trạng thái: v0.1.0 (2026-11-11). Tài liệu này ghi **cái gì** và **vì sao**. Mỗi quyết định kèm lý do đã bàn khi thiết kế, để người sau không phải đoán.

## 1. Vấn đề

Chủ dùng hai hệ cạnh nhau:

- **Multica**: nơi đội agent làm việc (issue, project, Type, workflow). Mỗi Type chọn một workflow, workflow chọn đội.
- **Một app chat có bot AI**, ví dụ [OpenMausBot](https://github.com/milind-soni/openmausbot): chủ trò chuyện với các bot trưởng phòng, chốt việc trong room.

Hướng đã chốt: **app chat giao việc, Multica làm.** Bot tạo issue trong Multica, theo dõi rồi đọc kết quả qua comment. Trước đây không có đường nào để làm việc này một cách an toàn.

## 2. Mục tiêu và ngoài phạm vi

Mục tiêu:

1. Bot gửi được một yêu cầu vào workspace Multica, kèm gợi ý project và Type.
2. Bot theo dõi được việc mình đã gửi: trạng thái, Multica đã xếp vào đâu, câu hỏi của đội, lý do bị từ chối.
3. Bot trả lời được câu hỏi của agent nhận việc.
4. Bot **không làm được gì khác**: không sửa, không giao, không đóng, không xoá, không xem việc của người khác.
5. Cài, chạy, cập nhật và gỡ theo chuẩn chung (`init`, `doctor`, `update`), một lệnh là cài xong.

Ngoài phạm vi:

- Phân loại issue. Đây là việc của Multica: Clef / Triage và agent nhận việc.
- Chuyển issue giữa các workspace. Multica không hỗ trợ.
- Đẩy thông báo từ Multica về app chat. Bot tự hỏi bằng `get_issue_status` / `list_my_issues`.
- OAuth sign-in. Để sau, xem §10.

## 3. Luật nghiệp vụ (chủ chốt 2026-11-11)

| # | Luật | Lý do |
|---|---|---|
| L1 | Bot gửi vào **bất kỳ workspace, project, Type nào** nó được phép, nhưng project và Type chỉ là **gợi ý** | Bot đang ở giữa cuộc trao đổi với chủ nên thường biết việc thuộc đâu. Đó là tín hiệu tốt, nhưng bot có thể nhầm, hoặc bị nội dung bên ngoài dắt (prompt injection). Chỉ phía Multica biết luật của workspace |
| L2 | Issue tạo ra **không có project, không có Type, không label**, giao cho **agent nhận việc** của workspace (như lệnh `/issue` của chủ). Gợi ý ghi trong mô tả, mục "Gợi ý của người gửi", và trong metadata `intake.hint_project` / `intake.hint_type` | Type quyết định workflow, workflow chọn đội. Chép thẳng gợi ý vào trường là bỏ mất lớp kiểm tra duy nhất |
| L3 | Agent nhận việc **luôn phân loại**: gợi ý khớp Clef thì đặt luôn; khác thì chọn theo mô tả và ghi lý do; không chắc thì hỏi bot một lần qua comment | Gợi ý sai mà agent sửa lại là chuyện bình thường. Mỗi lần lệch là dịp làm rõ mô tả project hoặc Type |
| L4 | **Không phân loại được thì từ chối**: huỷ issue, kèm comment ghi lý do, thiếu gì và đề xuất. Không đoán, không để issue treo không project | Một issue không project thì không workflow nào nhận. Mỗi lần từ chối là dịp bàn luật cho loại việc đó |
| L5 | Một issue bị từ chối thì không mở lại. Bot báo chủ, hai bên bàn cho rõ, rồi **gửi issue mới** | Giữ dấu vết. `reply_to_issue` trên issue đã đóng trả `issue_closed` |
| L6 | Chủ duyệt mỗi lần gửi và mỗi lần trả lời, bằng thẻ duyệt của app chat (MausBot mức `ask`). Đọc thì không cần duyệt | MausBot chỉ cho "Always allow" với tool chỉ đọc; tool tạo hoặc sửa luôn hỏi. Vì vậy 3 tool đọc được đánh dấu `readOnlyHint` |
| L7 | Workspace sai: agent nhận việc comment rồi huỷ; bot gửi lại vào workspace đúng | Multica không có phân loại workspace và không chuyển được issue sang workspace khác |

## 4. Năm tool

| Tool | Chỉ đọc | Vào | Ra |
|---|---|---|---|
| `list_workspaces` | ✓ | — | workspace được gửi (slug, tên, mô tả, agent nhận việc), project (tên, mô tả), Type (các lựa chọn) |
| `submit_issue` | | `workspace`, `title`, `request`, `hints{project,type}`, `request_id?` | `issue`, `url`, `assigned_to`, `already_sent`, `notes` |
| `get_issue_status` | ✓ | `issue` | `state` (open / done / rejected), `status`, `project`, `type`, `assigned_to`, `comments` (20 cái gần nhất), `needs_reply`, `rejection_reason` |
| `reply_to_issue` | | `issue`, `text` | `posted`, `notified` |
| `list_my_issues` | ✓ | `state?` (open / closed / all), `workspace?`, `limit?` | danh sách issue của bot |

Tên tool (chủ chốt): dùng chữ `issue` như Multica; động từ đứng trước, snake_case. `submit` mà không `create`, vì bot chỉ gửi vào, Multica mới là bên xếp.

Chi tiết:

- **Gợi ý được so với dữ liệu thật.** Không phân biệt hoa thường, khớp thì chuẩn hoá về đúng tên. Tên không có thật vẫn được giữ làm chữ, đánh dấu "(không có trong workspace này)", và bot nhận một `note`.
- **Gửi lại an toàn.**
  - `request_id` do bot đặt: gửi lại cùng id thì trả về issue cũ.
  - Multica tự chặn issue mở trùng tiêu đề (409 `active_duplicate_issue`). Nếu issue trùng là của chính bot, `already_sent: true`. Nếu của người khác, lỗi `duplicate`.
- **`needs_reply`**: issue còn mở và comment mới nhất không phải của bot.
- **`rejection_reason`**: issue bị huỷ và comment cuối là của người hoặc agent khác.
- **Trả lời đánh thức agent.** `reply_to_issue` gắn mention `[@Tên](mention://agent/<id>)` của agent đang giữ issue, giống cách bản vá decisions của fork làm cho câu trả lời của chủ.
- **Lỗi** luôn có dạng `{"error":{"code","why","fix"}}`, mã ổn định:
  - `unauthorized`, `bad_input`, `unknown_workspace`, `no_intake_agent`, `not_found`;
  - `issue_closed`, `duplicate`, `multica_error`, `multica_unreachable`, `partially_done`.

## 5. Danh tính: ai là ai

| Ai | Là gì |
|---|---|
| Chủ | Người thật, chịu trách nhiệm. Duyệt mỗi yêu cầu ở app chat |
| Bot (vd. Maya) | AI trong app chat. Multica không biết đến nó |
| Tài khoản bot (vd. `maus-maya`) | **Tài khoản member** trong Multica, chỉ dùng cho bot đó |
| multica-intake | Quầy lễ tân: ép luật, không lưu gì |
| Agent nhận việc | Agent Multica phân loại và giao việc |

**Quyết định: mỗi bot trưởng một tài khoản Multica.** Bot gửi PAT của tài khoản đó trong header `Authorization: Bearer`. multica-intake hỏi `GET /api/me` để biết đó là ai, rồi dùng chính PAT đó cho lần gọi.

Các phương án đã cân:

| Phương án | Vì sao không chọn |
|---|---|
| Một tài khoản chung `maus-opc`, phân biệt bot bằng metadata `source_bot` | Multica không biết bot nào gửi. Phải có key riêng cho từng bot, multica-intake phải giữ bảng hash. Thêm trạng thái, thêm bí mật để quản |
| Mỗi bot một key tự sinh (HMAC hoặc bảng hash) | Như trên, và phải tự viết cơ chế thu hồi |
| Bot là agent Multica (như Hermes) | Agent không có PAT. Nó chỉ nhận task token khi **Multica giao task**. Luồng này ngược lại: bot từ ngoài gửi vào |
| Admin tạo PAT thay cho tài khoản bot | Multica không cho: PAT luôn của chính người đang đăng nhập, và chỉ người thật mới tạo được (`RequireHumanActor`, MUL-8154) |
| OAuth | Đúng chuẩn MCP, nhưng multica-intake phải tự làm auth server. Multica gốc chưa có OAuth (upstream mới có một nhánh OAuth chờ sau PR #9690). Để sau |

Nguyên tắc rút ra: **danh tính do hệ giữ dữ liệu cấp**, không để bên gọi tự khai. Với bên gọi từ ngoài vào, Multica hiện chỉ có một loại giấy tờ là PAT của member.

### 5.1 Định danh issue của bot

Một issue là "của bot" khi **cả hai** điều kiện đúng:

1. `creator_type = member` và `creator_id` là id tài khoản của bot. Multica tự ghi, vì multica-intake **không bao giờ** gửi `X-Agent-ID` hay `X-Task-ID`, nên Multica luôn coi người gọi là member.
2. `metadata.source` bằng `source` trong cấu hình (vd. `mausbot`).

`list_my_issues` lọc ngay ở Multica (`creator_id` + `metadata`, có index GIN), rồi kiểm lại ở phía mình. `get_issue_status` và `reply_to_issue` kiểm đúng hai điều kiện trên. Issue không đạt thì trả `not_found`, như thể không tồn tại. Member và agent sửa được metadata, nhưng đó là người trong nhà. Bot thì không, vì không có tool nào sửa metadata.

### 5.2 Bộ nhớ của bot không phải nguồn trạng thái

Bot MausBot có bộ nhớ riêng (`MEMORY.md`, log hằng ngày, `session_search`) và có thể tự ghi "đã gửi STU-52". Nhưng bộ nhớ có thể không ghi, bị đẩy sang `archive.md`, bị sửa, và không biết khi agent đã xử. Trạng thái thật luôn lấy từ Multica qua `list_my_issues` / `get_issue_status`.

## 6. Nối với MausBot

### 6.1 Vì sao MCP dạng URL

| Đường | Kết quả |
|---|---|
| Bot gọi API hay CLI `multica` trong lượt chạy | Không được. Bot box (bwrap) chỉ cho ra địa chỉ công khai. MCP dạng lệnh (stdio) cũng chạy trong box nên cũng bị chặn |
| Webhook autopilot của Multica | Cùng lý do, và chỉ bắn một autopilot định sẵn |
| **MCP server dạng URL** | **Được.** Broker của MausBot gọi URL từ tiến trình server, ở ngoài box |

### 6.2 Broker là gì, PAT nằm ở đâu

- **Broker** là reverse proxy gắn bí mật, nằm trong server MausBot, ngoài box.
  - Đầu mỗi lượt chạy, server viết lại cấu hình MCP đưa vào box: URL thành `127.0.0.1:<broker>/b/<token tạm>/…`, header thành rỗng. PAT được giao cho broker, gắn với token tạm (đúng bot, đúng cuộc trò chuyện, có hạn).
  - Khi bot gọi, broker tra token tạm, xoá header xác thực do bot tự gửi, gắn PAT rồi chuyển tới đúng địa chỉ cố định.
- **PAT nằm ở đúng một chỗ**: `config.json` của MausBot (mục MCP, header write-only trong UI), chữ thường, quyền đọc chỉ của chủ. multica-intake không lưu gì. Multica chỉ lưu bản băm.
- **Mỗi bot một mục MCP** (vd. `multica-maus-maya`), cùng trỏ tới một multica-intake, chỉ khác PAT. MausBot không có tính năng "bot X dùng tài khoản Y": docs ghi "grants … do not pick an account", muốn tách thì dùng "a separate configuration". Mỗi mục MCP chính là một configuration như vậy. Tên mục chỉ để người đọc. **Ranh giới thật là Bot access**: mục nào giao cho bot nào thì bot đó dùng được.
- **Bẫy:** MCP thêm trong Settings mặc định thuộc **mọi bot**. Bot không có danh sách riêng thì thấy hết. Phải đặt Bot access cho từng mục, và có check báo lệch (phía fgOPC).
- **Backup:** header nằm trong `config.json`, nên backup volume MausBot có chứa PAT. Token OAuth thì MausBot để riêng và tự loại khỏi backup. Cách xử phía fgOPC: loại `config.json` khỏi backup, khôi phục xong thì cấp PAT mới.

### 6.3 Vì sao không dùng MCP có sẵn (khảo sát 2026-11-11)

| MCP | Vì sao không |
|---|---|
| `strider2038/multica-mcp` (Go, 15 tool) | Có update, assign, đổi status. Viết cho API v0.3.31 |
| `zxdwhda/multica-mcp` (Go, 395 tool) | Có tool gọi API tuỳ ý, tương đương đưa thẳng PAT cho bot |
| `Korkyzer/multica-mcp` (TS) | Chỉ có stdio, broker không gọi được |
| `DonShelly/multica-mcp` (Python) | Có xoá issue, sửa autopilot. Bỏ từ tháng 5, không license |
| `Yidada/multica-chatgpt-connector` | Chỉ chạy với Multica Cloud |
| upstream PR #9690 (Python) | Chặn theo lệnh CLI, không theo flag: đã cho `issue create` là bot đặt được `--project` và `--assignee`. Chưa merge |

Lý do chung: Multica **không có PAT giới hạn quyền**. Tài khoản bot làm được mọi thứ một member làm được. Chỗ duy nhất giữ được luật là chính MCP. Tool nào có trong MCP thì bot gọi được, kể cả khi bot bị nội dung bên ngoài dắt. Mấy MCP kia làm cho người dùng tin cậy. Ở đây bên gọi là bot đọc nội dung từ ngoài, nên MCP phải là ranh giới quyền, và hẹp đúng bằng luật.

Học từ #9690 phần vỏ an toàn:
- mặc định cấm hết;
- giới hạn workspace;
- audit mỗi lần gọi;
- che token;
- timeout và giới hạn cỡ đầu vào.

### 6.4 Đăng nhập tài khoản bot (phía vận hành)

Multica tự host đăng nhập bằng email + mã. Bật SMTP cho Multica, rồi dùng **email có dấu `+`** trỏ về hộp thư của chủ (`<chủ>+maus-maya@<domain>`): mỗi bot là một tài khoản riêng, nhưng mọi mã đăng nhập về một hộp thư. Thêm một bot, khoảng 3 phút, làm hết trên UI:

1. Mời email đó vào workspace.
2. Đăng nhập trong cửa sổ ẩn danh, nhận lời mời.
3. Settings → Tokens: tạo PAT.
4. Dán PAT vào mục MCP của MausBot, chọn Bot access.

Gia hạn: `POST /api/tokens/current/renew` kéo dài thêm 90 ngày mà **không đổi chuỗi**, nên không phải dán lại và các "Always allow" vẫn giữ. Gia hạn do chủ chạy, không tự động, để hạn PAT vẫn có ý nghĩa.

## 7. Kỹ thuật

- **Go**, một binary tĩnh (`CGO_ENABLED=0`). SDK chính thức `github.com/modelcontextprotocol/go-sdk` v1.8.0. Chọn Go vì cùng ngôn ngữ với Multica, image nhỏ, không cần runtime.
- **Gọi thẳng API REST** của Multica, không qua CLI: ít hàm, dễ kiểm, không cần CLI trong image. Danh sách lời gọi nằm hết trong `internal/multica`. Body tạo issue chỉ có `title`, `description`, `status`, `assignee_*`.
- **Streamable HTTP, stateless, trả JSON.** Mỗi request tự xác thực, nên đi qua proxy dễ. Có thêm stdio cho dùng cục bộ (đọc PAT từ file 0600).
- **Không trạng thái.** Chỉ cache kết quả `/api/me` 2 phút theo băm của PAT, nên PAT bị thu hồi thì tối đa 2 phút sau hết dùng được.
- **Audit**: mỗi lần gọi một dòng JSON ra stderr: account, tool, workspace, issue, ok, mã lỗi, thời gian. Không bao giờ ghi nội dung hay token.
- **Mặc định nghe loopback**; địa chỉ khác thì in cảnh báo. Giới hạn body 1 MiB, mỗi lời gọi Multica có timeout, không theo redirect khi mang PAT.
- **Cài đặt** theo chuẩn `cs-packaging-distribution`: `install.sh` một dòng, `init` / `doctor` / `update` / `uninstall`, `--json` với một khung chung, self-update có kiểm SHA-256 và giữ bản rollback, user service systemd. Đối chiếu từng luật trong [packaging-checklist.md](packaging-checklist.md).
- **Image** `ghcr.io/vantt/multica-intake`: distroless, uid **61103** (khối 611xx của stack MausBot trong fgOPC, ngoài vùng `useradd`), cấu hình bằng biến môi trường `MULTICA_INTAKE_*`.

## 8. Triển khai trong fgOPC

Container `multica-intake` chạy chung namespace mạng với container MausBot (`omb`), giống Caddy, và chỉ nghe `127.0.0.1:8801`. Broker gọi `http://127.0.0.1:8801/mcp`. Không phải đổi bảng nft, vì multica-intake đi ra API Multica bằng cùng đường đã được phép. Cấu hình (`source: mausbot`, `locale: vi`, `intake_agent`, `app_url`) nằm trong compose của fgOPC.

## 9. Tiêu chí xong

1. Năm tool chạy trên Multica thật (fork v0.7.2-vantt.20):
   - gửi việc kèm gợi ý đúng và sai;
   - gửi lại cùng `request_id`, gửi trùng tiêu đề;
   - bot thứ hai không thấy và không trả lời được issue của bot thứ nhất;
   - issue của chủ có cùng `source` vẫn không thấy;
   - issue bị huỷ ra `rejected` kèm lý do, và `reply_to_issue` trên nó bị từ chối.
2. Test đơn vị với Multica giả bao các luật trên, cộng việc body tạo issue không bao giờ mang project, Type hay label, và không request nào mang `X-Agent-ID`.
3. Cài một dòng từ release công khai, `init --yes --json` rồi `doctor` chạy được không cần README. Test vòng đời bộ cài chạy offline.
4. Release có đủ 4 archive, `install.sh` đóng dấu phiên bản, `checksums.txt`, và image hai kiến trúc trên GHCR.

## 10. Việc sau

- **OAuth.** Khi Multica (hoặc PR #9690 cùng nhánh OAuth) có, chuyển sang. Mỗi bot một địa chỉ (`/mcp/<bot>`), vì token OAuth của MausBot gắn với địa chỉ server.
- **Triage của Multica gốc** (`issue.triage_state`). Khi có, `submit_issue` chuyển sang tạo issue ở trạng thái chờ triage, thay cho việc giao thẳng cho agent nhận việc.
- **MCP chính thức** (upstream #1351, #5125). Nếu có, multica-intake có thể chỉ còn là lớp ép luật "chỉ gợi ý" đứng trước.
- **Ký release** (cosign keyless, SBOM) khi có workflow release trên GitHub Actions.
