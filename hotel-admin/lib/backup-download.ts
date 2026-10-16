// Kept out of the page module: a Next.js page file may export only the page and its config.
/** Posts the ticket as a form so the browser saves the file in place (Content-Disposition: attachment). */
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
