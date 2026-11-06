import { useCallback, useEffect, useRef, useState, type DragEvent } from "react";
import { collectPastedFiles, hasTransferFiles, normalizePastedFiles } from "../services/pasteFiles";

export function useFileDrop({ disabled, onFiles }: {
  disabled: boolean;
  onFiles: (files: File[]) => void;
}) {
  const [isDragging, setIsDragging] = useState(false);
  const dragDepth = useRef(0);

  const reset = useCallback(() => {
    dragDepth.current = 0;
    setIsDragging(false);
  }, []);

  useEffect(() => {
    if (disabled) reset();
  }, [disabled, reset]);

  const onDragEnterCapture = useCallback((event: DragEvent<HTMLElement>) => {
    if (!hasTransferFiles(event.dataTransfer)) return;
    event.preventDefault();
    event.stopPropagation();
    if (disabled) return;
    dragDepth.current += 1;
    setIsDragging(true);
  }, [disabled]);

  const onDragOverCapture = useCallback((event: DragEvent<HTMLElement>) => {
    if (!hasTransferFiles(event.dataTransfer)) return;
    event.preventDefault();
    event.stopPropagation();
    event.dataTransfer.dropEffect = disabled ? "none" : "copy";
  }, [disabled]);

  const onDragLeaveCapture = useCallback((event: DragEvent<HTMLElement>) => {
    if (dragDepth.current === 0) return;
    event.stopPropagation();
    dragDepth.current = Math.max(0, dragDepth.current - 1);
    if (dragDepth.current === 0) setIsDragging(false);
  }, []);

  const onDropCapture = useCallback((event: DragEvent<HTMLElement>) => {
    if (!hasTransferFiles(event.dataTransfer)) return;
    event.preventDefault();
    event.stopPropagation();
    reset();
    if (disabled) return;
    // DataTransfer 仅在事件期间可读；异步读取文件头前先取出所有 File。
    const files = collectPastedFiles(event.dataTransfer);
    if (files.length > 0) void normalizePastedFiles(files).then(onFiles);
  }, [disabled, onFiles, reset]);

  return {
    isDragging,
    dropHandlers: { onDragEnterCapture, onDragOverCapture, onDragLeaveCapture, onDropCapture },
  };
}
