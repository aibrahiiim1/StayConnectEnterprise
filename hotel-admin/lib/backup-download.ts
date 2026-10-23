// Next.js pages cannot export this helper; form submission lets the browser save the attachment.
export function submitDownload(name: string, ticket: string) {
  const form = document.createElement("form");
  form.method = "POST";
  form.action = `/api/edge/v1/backup-download/${encodeURIComponent(name)}`;
  form.style.display = "none";
  const input = document.createElement("input");
  input.type = "hidden";
  input.name = "ticket";
  input.value = ticket;
  form.appendChild(input);
  document.body.appendChild(form);
  try { form.submit(); } finally { form.remove(); }
}
