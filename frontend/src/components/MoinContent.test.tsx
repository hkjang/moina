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

  it("http와 https 주소를 새 탭 링크로 만든다", () => {
    render(<MemoryRouter><MoinContent content="자료는 https://moina.example/guide 에 있습니다" moinId="m1"/></MemoryRouter>);
    const link = screen.getByRole("link", { name: "https://moina.example/guide" });
    expect(link).toHaveAttribute("href", "https://moina.example/guide");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer nofollow");
  });

  it("http와 https가 아닌 주소는 링크로 만들지 않는다", () => {
    for (const content of ["javascript:alert(1)", "data:text/html,<b>x</b>", "ftp://moina.example/x"]) {
      expect(moinContentParts(content).filter((part) => part.type === "link")).toEqual([]);
    }
  });

  it("주소 안의 @와 #를 멘션이나 토픽으로 떼어내지 않는다", () => {
    expect(moinContentParts("https://moina.example/@alice#guide").filter((part) => part.type !== "text"))
      .toEqual([{ type: "link", value: "https://moina.example/@alice#guide", target: "https://moina.example/@alice#guide" }]);
  });

  it("문장 끝 구두점은 주소에서 떼어내고 짝이 맞는 괄호는 남긴다", () => {
    expect(moinContentParts("https://moina.example/a. 확인").filter((part) => part.type === "link"))
      .toEqual([{ type: "link", value: "https://moina.example/a", target: "https://moina.example/a" }]);
    expect(moinContentParts("(https://moina.example/a)").filter((part) => part.type === "link"))
      .toEqual([{ type: "link", value: "https://moina.example/a", target: "https://moina.example/a" }]);
    expect(moinContentParts("https://moina.example/a_(b) 확인").filter((part) => part.type === "link"))
      .toEqual([{ type: "link", value: "https://moina.example/a_(b)", target: "https://moina.example/a_(b)" }]);
  });

  it("주소 바로 뒤에 붙은 한글은 주소에 포함하지 않는다", () => {
    expect(moinContentParts("https://moina.example/a에서 받으세요"))
      .toEqual([
        { type: "link", value: "https://moina.example/a", target: "https://moina.example/a" },
        { type: "text", value: "에서 받으세요" },
      ]);
  });

  it("주소 뒤에 이어지는 멘션과 토픽도 그대로 인식한다", () => {
    expect(moinContentParts("https://moina.example/a. @bob_user #공지").filter((part) => part.type !== "text"))
      .toEqual([
        { type: "link", value: "https://moina.example/a", target: "https://moina.example/a" },
        { type: "mention", value: "@bob_user", target: "bob_user" },
        { type: "topic", value: "#공지", target: "공지" },
      ]);
  });
});
