import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { MoinContent, moinContentParts } from "./MoinContent";

describe("MoinContent", () => {
  it("멘션과 해시태그를 프로필과 토픽 링크로 만든다", () => {
    render(<MemoryRouter><MoinContent content="안녕 @Alice #데이터" moinId="m1"/></MemoryRouter>);
    expect(screen.getByRole("link", { name: "@Alice" })).toHaveAttribute("href", "/profile/Alice");
    expect(screen.getByRole("link", { name: "#데이터" })).toHaveAttribute("href", "/topics/%EB%8D%B0%EC%9D%B4%ED%84%B0");
    expect(screen.getAllByRole("link")).toHaveLength(3);
  });

  it("이메일과 단어 중간 @는 멘션으로 처리하지 않는다", () => {
    expect(moinContentParts("mail@example.com 붙여쓴@alice @bob_user").filter((part) => part.type === "mention"))
      .toEqual([{ type: "mention", value: "@bob_user", target: "bob_user" }]);
  });
});
