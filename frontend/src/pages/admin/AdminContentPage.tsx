import { RefreshCw, Search, Trash2 } from "lucide-react";
import { useState } from "react";
import { apiRequest, readableError } from "../../api/client";
import { useToast } from "../../components/ToastProvider";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  LoadingState,
} from "../../components/ui";
import { useApiQuery } from "../../hooks/useApiQuery";
import { formatDate, listFrom } from "../../utils/format";
import { AdminTitle, Table } from "./components";
import { statusTone } from "./helpers";

interface AdminPost {
  id: string;
  content?: string;
  authorUsername?: string;
  status?: string;
  reportCount?: number;
  createdAt?: string;
}
export function AdminContentPage() {
  const { notify } = useToast();
  const query = useApiQuery<unknown>("/admin/posts?limit=100");
  const [search, setSearch] = useState("");
  const [working, setWorking] = useState<string | null>(null);
  const posts = listFrom<AdminPost>(
    query.data as AdminPost[] | { items?: AdminPost[] } | undefined,
  ).filter((post) =>
    `${post.content} ${post.authorUsername}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  );
  const remove = async (post: AdminPost) => {
    if (!window.confirm("이 모인을 운영 정책에 따라 삭제할까요?")) return;
    setWorking(post.id);
    try {
      await apiRequest(`/admin/posts/${encodeURIComponent(post.id)}`, {
        method: "PATCH",
        body: { status: "deleted" },
      });
      notify(
        "모인을 삭제 상태로 변경하고 감사 로그에 기록했습니다.",
        "success",
      );
      query.reload();
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      setWorking(null);
    }
  };
  return (
    <div className="page-stack">
      <AdminTitle
        title="콘텐츠 관리"
        description="모인과 커뮤니티 콘텐츠를 검색하고 운영 정책에 따라 조치합니다."
      />
      <Card>
        <div className="table-toolbar">
          <label className="table-search">
            <Search />
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              aria-label="콘텐츠 검색"
              placeholder="내용 또는 작성자 검색"
            />
          </label>
          <Button onClick={query.reload}>
            <RefreshCw />
            새로고침
          </Button>
        </div>
        {query.loading ? (
          <LoadingState />
        ) : query.error ? (
          <ErrorState message={query.error} onRetry={query.reload} />
        ) : posts.length ? (
          <Table
            caption="모인 관리 목록"
            headers={["내용", "작성자", "신고", "상태", "작성일", "조치"]}
          >
            {posts.map((post) => (
              <tr key={post.id}>
                <td>
                  <strong className="table-content">
                    {post.content || "내용 없음"}
                  </strong>
                  <small>{post.id}</small>
                </td>
                <td>@{post.authorUsername || "알 수 없음"}</td>
                <td>{post.reportCount || 0}건</td>
                <td>
                  <Badge tone={statusTone(post.status || "active")}>
                    {post.status || "게시 중"}
                  </Badge>
                </td>
                <td>{formatDate(post.createdAt)}</td>
                <td>
                  <Button
                    size="small"
                    variant="ghost"
                    className="text-danger"
                    onClick={() => void remove(post)}
                    disabled={working === post.id}
                  >
                    <Trash2 />
                    삭제
                  </Button>
                </td>
              </tr>
            ))}
          </Table>
        ) : (
          <EmptyState
            title="관리할 콘텐츠가 없습니다"
            description="새 모인이 게시되면 여기에 표시됩니다."
          />
        )}
      </Card>
    </div>
  );
}
