// Экспорт данных админ-панели (ТЗ §13, T14.4): CSV-файл и печать в PDF.

// csvEscape quotes a field per RFC 4180 when it contains special characters.
function csvEscape(value: unknown): string {
  const s = value === null || value === undefined ? "" : String(value);
  if (/[",\n\r]/.test(s)) {
    return `"${s.replace(/"/g, '""')}"`;
  }
  return s;
}

// buildCsv renders rows (arrays aligned with headers) into CSV text.
export function buildCsv(headers: string[], rows: unknown[][]): string {
  const lines = [headers.map(csvEscape).join(";")];
  for (const row of rows) {
    lines.push(row.map(csvEscape).join(";"));
  }
  return lines.join("\r\n");
}

// downloadCsv triggers a browser download of the CSV text. A UTF-8 BOM makes
// Excel open Cyrillic text correctly.
export function downloadCsv(filename: string, headers: string[], rows: unknown[][]) {
  const content = "\uFEFF" + buildCsv(headers, rows);
  const blob = new Blob([content], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  URL.revokeObjectURL(url);
}

// printTable opens the browser print dialog (Save as PDF) for the current page.
export function printPage() {
  window.print();
}
