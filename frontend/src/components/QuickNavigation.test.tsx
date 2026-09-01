import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { rememberRecentRoute } from "../config";
import { QuickNavigation } from "./QuickNavigation";

afterEach(() => cleanup());

function CurrentLocation() {
  const location = useLocation();
  return <output aria-label="현재 경로">{location.pathname}{location.search}</output>;
}

function Harness({
  permissions = ["*"],
  userId = "quick-navigation-user",
}: {
  permissions?: string[];
  userId?: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>빠른 이동 열기</button>
      <QuickNavigation
        open={open}
        onOpenChange={setOpen}
        userId={userId}
        username="navigator"
        permissions={permissions}
        approvalVisible
      />
      <CurrentLocation />
    </>
  );
}

function renderNavigation(options: Parameters<typeof Harness>[0] = {}) {
  return render(
    <MemoryRouter initialEntries={["/flow"]}>
      <Harness {...options} />
    </MemoryRouter>,
  );
}

describe("빠른 이동 팔레트", () => {
  it("Ctrl+K로 열고 검색·방향키·Enter로 화면을 이동한다", async () => {
    renderNavigation();
    fireEvent.keyDown(window, { key: "k", ctrlKey: true });

    const dialog = await screen.findByRole("dialog", { name: "빠른 이동" });
    const input = within(dialog).getByRole("combobox", { name: "빠른 이동 검색" });
    await waitFor(() => expect(input).toHaveFocus());
    fireEvent.change(input, { target: { value: "포켓" } });
    expect(within(dialog).getByRole("option", { name: /“포켓” 통합 검색/ })).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(input, { key: "ArrowDown" });
    const pocket = within(dialog).getByRole("option", { name: /포켓.*나중에 볼 모인/ });
    expect(pocket).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => expect(screen.getByLabelText("현재 경로")).toHaveTextContent("/pocket"));
    expect(screen.queryByRole("dialog", { name: "빠른 이동" })).not.toBeInTheDocument();
  });

  it("검색어 자체를 통합 검색으로 즉시 보낸다", async () => {
    renderNavigation();
    fireEvent.click(screen.getByRole("button", { name: "빠른 이동 열기" }));
    const input = await screen.findByRole("combobox", { name: "빠른 이동 검색" });
    fireEvent.change(input, { target: { value: "분산 시스템" } });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => expect(screen.getByLabelText("현재 경로")).toHaveTextContent("/search?q=%EB%B6%84%EC%82%B0%20%EC%8B%9C%EC%8A%A4%ED%85%9C&type=posts"));
  });

  it("Escape로 닫으면 팔레트를 열기 전 초점을 복원한다", async () => {
    renderNavigation();
    const trigger = screen.getByRole("button", { name: "빠른 이동 열기" });
    trigger.focus();
    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    const dialog = await screen.findByRole("dialog", { name: "빠른 이동" });
    await waitFor(() => expect(within(dialog).getByRole("combobox")).toHaveFocus());
    fireEvent.keyDown(document.activeElement || window, { key: "Escape" });
    await waitFor(() => expect(dialog).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();
  });

  it("최근 방문한 동적 콘텐츠를 안전하게 다시 제시한다", async () => {
    const userId = "quick-navigation-recent-user";
    rememberRecentRoute(userId, "/topics/react-query");
    rememberRecentRoute(userId, "/profile/alice");
    renderNavigation({ userId });
    fireEvent.click(screen.getByRole("button", { name: "빠른 이동 열기" }));

    const recent = await screen.findByRole("group", { name: "최근 방문" });
    expect(within(recent).getByRole("option", { name: /@alice 프로필/ })).toBeInTheDocument();
    expect(within(recent).getByRole("option", { name: /#react-query 토픽/ })).toBeInTheDocument();
  });

  it("권한이 없는 AI와 관리자 화면은 결과에서 제외한다", async () => {
    renderNavigation({ permissions: [] });
    fireEvent.click(screen.getByRole("button", { name: "빠른 이동 열기" }));
    const dialog = await screen.findByRole("dialog", { name: "빠른 이동" });

    expect(within(dialog).queryByRole("option", { name: /^AI/ })).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("group", { name: "서비스 관리자" })).not.toBeInTheDocument();
  });

  it("G 연속 단축키와 C 작성 단축키를 입력 중이 아닐 때만 실행한다", async () => {
    renderNavigation();
    fireEvent.keyDown(window, { key: "g" });
    expect(screen.getByText("다음 이동 키를 누르세요")).toBeInTheDocument();
    fireEvent.keyDown(window, { key: "m" });
    await waitFor(() => expect(screen.getByLabelText("현재 경로")).toHaveTextContent("/moims"));

    const input = document.createElement("input");
    document.body.append(input);
    input.focus();
    fireEvent.keyDown(input, { key: "c" });
    expect(screen.getByLabelText("현재 경로")).toHaveTextContent("/moims");
    input.remove();

    fireEvent.keyDown(window, { key: "c" });
    await waitFor(() => expect(screen.getByLabelText("현재 경로")).toHaveTextContent("/flow?compose=1"));
  });
});
