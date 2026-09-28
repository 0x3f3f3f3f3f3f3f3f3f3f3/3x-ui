import { afterEach, beforeEach, vi } from 'vitest';
import { act } from '@testing-library/react';
import { unmount } from '@rc-component/util';
import { message } from 'antd';
import { actDestroy } from 'antd/es/message';

export function setupStaticMessageCleanup() {
  const fragments = new Set<DocumentFragment>();
  const createFragment = document.createDocumentFragment.bind(document);
  let restore: () => void;

  beforeEach(() => {
    const spy = vi.spyOn(document, 'createDocumentFragment').mockImplementation(() => {
      const fragment = createFragment();
      fragments.add(fragment);
      return fragment;
    });
    restore = () => spy.mockRestore();
  });

  afterEach(async () => {
    await act(async () => message.destroy());
    // Closing notices retains AntD's static root and its pending animation work.
    await act(async () => {
      for (const fragment of fragments) await unmount(fragment);
    });
    actDestroy();
    fragments.clear();
    restore();
  });
}
