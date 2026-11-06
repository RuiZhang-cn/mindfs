// 剪贴板里的文件（如微信“复制图片”写入的 CF_HDROP）在浏览器里表现为
// kind === "file" 的条目；扩展名不被系统识别时 MIME 为空，需要按文件头嗅探。

type ImageSignature = {
  mime: string;
  ext: string;
  match: (head: Uint8Array) => boolean;
};

const HEAD_BYTES = 16;

function startsWith(head: Uint8Array, bytes: number[], offset = 0): boolean {
  if (head.length < offset + bytes.length) {
    return false;
  }
  return bytes.every((value, index) => head[offset + index] === value);
}

function ascii(head: Uint8Array, offset: number, text: string): boolean {
  if (head.length < offset + text.length) {
    return false;
  }
  for (let index = 0; index < text.length; index += 1) {
    if (head[offset + index] !== text.charCodeAt(index)) {
      return false;
    }
  }
  return true;
}

const IMAGE_SIGNATURES: ImageSignature[] = [
  { mime: "image/png", ext: "png", match: (head) => startsWith(head, [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]) },
  { mime: "image/jpeg", ext: "jpg", match: (head) => startsWith(head, [0xff, 0xd8, 0xff]) },
  { mime: "image/gif", ext: "gif", match: (head) => ascii(head, 0, "GIF8") },
  { mime: "image/webp", ext: "webp", match: (head) => ascii(head, 0, "RIFF") && ascii(head, 8, "WEBP") },
  { mime: "image/bmp", ext: "bmp", match: (head) => ascii(head, 0, "BM") },
];

export function sniffImageType(head: Uint8Array): { mime: string; ext: string } | null {
  for (const signature of IMAGE_SIGNATURES) {
    if (signature.match(head)) {
      return { mime: signature.mime, ext: signature.ext };
    }
  }
  return null;
}

function fileExtension(name: string): string {
  const index = name.lastIndexOf(".");
  if (index <= 0 || index === name.length - 1) {
    return "";
  }
  return name.slice(index + 1).toLowerCase();
}

function withExtension(name: string, ext: string): string {
  const index = name.lastIndexOf(".");
  const base = index > 0 ? name.slice(0, index) : name;
  return `${base || "pasted-image"}.${ext}`;
}

// dragenter/dragover 处于浏览器的 protected mode，只能检查类型，不能读取文件。
export function hasTransferFiles(dataTransfer: DataTransfer | null | undefined): boolean {
  return !!dataTransfer && (
    Array.from(dataTransfer.types || []).includes("Files")
    || Array.from(dataTransfer.items || []).some((item) => item.kind === "file")
    || dataTransfer.files.length > 0
  );
}

// 采集剪贴板事件里的全部文件：不再只认 image/*，微信与资源管理器的
// 文件剪贴板条目常常没有 MIME。
export function collectPastedFiles(dataTransfer: DataTransfer | null | undefined): File[] {
  if (!dataTransfer) {
    return [];
  }
  const fromItems: File[] = [];
  let hasDirectory = false;
  for (const item of Array.from(dataTransfer.items || [])) {
    if (item.kind !== "file") {
      continue;
    }
    if (item.webkitGetAsEntry?.()?.isDirectory) {
      hasDirectory = true;
      continue;
    }
    const file = item.getAsFile();
    if (file) {
      fromItems.push(file);
    }
  }
  if (fromItems.length > 0 || hasDirectory) {
    return fromItems;
  }
  return Array.from(dataTransfer.files || []);
}

// 补全缺失/错误的 MIME 与扩展名：微信复制的图片可能叫 xxx.dat 且 type 为空，
// 直接上传会让 agent 收到无法预览的 .dat。嗅探文件头后按真实格式改名。
export async function normalizePastedFile(file: File): Promise<File> {
  if (file.type.startsWith("image/")) {
    return file;
  }
  let head: Uint8Array;
  try {
    head = new Uint8Array(await file.slice(0, HEAD_BYTES).arrayBuffer());
  } catch {
    return file;
  }
  const sniffed = sniffImageType(head);
  if (!sniffed) {
    return file;
  }
  const extension = fileExtension(file.name);
  const matchesExtension = extension === sniffed.ext || (sniffed.ext === "jpg" && extension === "jpeg");
  return new File(
    [file],
    matchesExtension ? file.name : withExtension(file.name, sniffed.ext),
    { type: sniffed.mime, lastModified: file.lastModified || Date.now() },
  );
}

export async function normalizePastedFiles(files: File[]): Promise<File[]> {
  return Promise.all(files.map((file) => normalizePastedFile(file)));
}

