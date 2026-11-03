import { describe, expect, it } from "vitest";
import { buildCsv } from "./export";

describe("buildCsv", () => {
  it("соединяет строки с разделителем ;", () => {
    const csv = buildCsv(["a", "b"], [[1, "x"]]);
    expect(csv).toBe("a;b\r\n1;x");
  });

  it("экранирует кавычки, запятые и переносы строк", () => {
    const csv = buildCsv(["h"], [[`"quoted", value\nnew`]]);
    expect(csv).toBe('h\r\n"""quoted"", value\nnew"');
  });

  it("превращает null и undefined в пустые ячейки", () => {
    const csv = buildCsv(["a", "b"], [[null, undefined]]);
    expect(csv).toBe("a;b\r\n;");
  });
});
