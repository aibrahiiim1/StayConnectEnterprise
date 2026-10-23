/** Save the inner document because Hotel Admin cannot import Central's response envelope. */
export function saveFile(filename: string, answer: unknown) {
  let text: string;
  if (typeof answer === "string") {
    text = answer;
  } else {
    const obj = answer as Record<string, unknown> | null;
    const doc = obj && typeof obj === "object" ? obj.package ?? obj.license ?? obj : obj;
    text = JSON.stringify(doc, null, 2);
  }
  const blob = new Blob([text], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
