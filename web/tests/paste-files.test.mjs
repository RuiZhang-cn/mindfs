import assert from "node:assert/strict";
import {
  collectPastedFiles,
  normalizePastedFile,
  hasTransferFiles,
  sniffImageType,
} from "../src/services/pasteFiles.ts";

const PNG_BYTES = Uint8Array.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d]);
const JPEG_BYTES = Uint8Array.from([0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46]);
const WEBP_BYTES = Uint8Array.from([
  0x52, 0x49, 0x46, 0x46, 0x24, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50, 0x56, 0x50, 0x38, 0x20,
]);

assert.deepEqual(sniffImageType(PNG_BYTES), { mime: "image/png", ext: "png" });
assert.deepEqual(sniffImageType(JPEG_BYTES), { mime: "image/jpeg", ext: "jpg" });
assert.deepEqual(sniffImageType(WEBP_BYTES), { mime: "image/webp", ext: "webp" });
assert.equal(sniffImageType(new Uint8Array([0x25, 0x50, 0x44, 0x46])), null, "PDF must not be treated as an image");

// Windows 文件拖放剪贴板（CF_HDROP）：条目没有 MIME，微信复制的图片常为 .dat。
const datFile = new File([PNG_BYTES], "wechat_image.dat", { type: "", lastModified: 1 });
const pdfFile = new File([new Uint8Array([0x25, 0x50, 0x44, 0x46])], "report.pdf", { type: "application/pdf" });

const dataTransfer = {
  items: [
    { kind: "file", type: "", getAsFile: () => datFile },
    { kind: "file", type: "application/pdf", getAsFile: () => pdfFile },
    { kind: "string", type: "text/plain", getAsFile: () => null },
  ],
  files: [],
};
assert.deepEqual(collectPastedFiles(dataTransfer), [datFile, pdfFile]);
assert.deepEqual(collectPastedFiles(null), []);
// 部分浏览器只填充 files，不回填 items。
assert.deepEqual(collectPastedFiles({ items: [], files: [pdfFile] }), [pdfFile]);

const normalized = await normalizePastedFile(datFile);
assert.equal(normalized.name, "wechat_image.png", "mislabelled extension should follow the sniffed type");
assert.equal(normalized.type, "image/png");
assert.equal(normalized.size, datFile.size, "file bytes must be preserved");
assert.equal(await normalized.text().catch(() => ""), await datFile.text().catch(() => ""));

const untouchedPdf = await normalizePastedFile(pdfFile);
assert.equal(untouchedPdf, pdfFile, "non-image files must pass through unchanged");

const typedImage = new File([PNG_BYTES], "already.png", { type: "image/png" });
assert.equal(await normalizePastedFile(typedImage), typedImage, "typed images must not be re-wrapped");

const jpegNamedJpeg = new File([JPEG_BYTES], "photo.jpeg", { type: "" });
assert.equal((await normalizePastedFile(jpegNamedJpeg)).name, "photo.jpeg", ".jpeg is already the right name for JPEG");

const extensionless = new File([PNG_BYTES], "clipboard", { type: "application/octet-stream" });
const renamed = await normalizePastedFile(extensionless);
assert.equal(renamed.name, "clipboard.png");
assert.equal(renamed.type, "image/png");

// 文件复制优先于伴随的文本元数据，不按 MIME、扩展名或文件大小筛选。
const emptyFile = new File([], "空文件.unknown", { type: "" });
const mixedTransfer = {
  types: ["Files", "text/plain"],
  items: [
    { kind: "file", getAsFile: () => pdfFile },
    { kind: "file", getAsFile: () => emptyFile },
  ],
  files: [pdfFile, emptyFile],
  getData: () => "copied file metadata",
};
assert.deepEqual(collectPastedFiles(mixedTransfer), [pdfFile, emptyFile]);
assert.equal(await normalizePastedFile(emptyFile), emptyFile);

// 拖拽悬停时浏览器禁止读取文件，只有 Files 类型；仍须允许放下。
assert.equal(hasTransferFiles({ types: ["Files"], items: [], files: [] }), true);
assert.equal(hasTransferFiles({ types: [], items: [], files: [emptyFile] }), true);
assert.equal(hasTransferFiles({ types: [], items: [{ kind: "file" }], files: [] }), true);
assert.equal(hasTransferFiles({ types: ["text/plain"], items: [{ kind: "string" }], files: [] }), false);
assert.equal(hasTransferFiles(null), false);

// 文件夹不是空文件；混拖时跳过目录，保留真实文件（含零字节文件）。
const directoryItem = { kind: "file", webkitGetAsEntry: () => ({ isDirectory: true }), getAsFile: () => new File([], "folder") };
assert.deepEqual(collectPastedFiles({ items: [directoryItem], files: [new File([], "folder")] }), []);
assert.deepEqual(collectPastedFiles({ items: [directoryItem, ...mixedTransfer.items], files: [] }), [pdfFile, emptyFile]);

console.log("paste-files: ok");
