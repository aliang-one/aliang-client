<template>
  <Teleport to="body">
    <div
      v-if="open"
      class="fixed inset-0 z-[210] flex items-center justify-center p-4"
      role="dialog"
      aria-modal="true"
      :aria-label="t('tutorialDocs_title')"
    >
      <div class="absolute inset-0 bg-slate-900/60" @click="emit('close')"></div>

      <div
        class="relative z-10 flex h-[min(88vh,760px)] w-[min(96vw,900px)] flex-col overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900"
      >
        <div class="flex items-start justify-between gap-4 border-b border-slate-100 px-5 py-4 dark:border-slate-800">
          <div>
            <p class="text-[11px] font-bold uppercase tracking-[0.24em] text-primary">{{ t('tutorialDocs_title') }}</p>
            <h3 class="mt-1 text-lg font-semibold text-slate-900 dark:text-white">{{ currentDocTitle }}</h3>
          </div>
          <button
            type="button"
            class="inline-flex size-8 shrink-0 items-center justify-center rounded-full text-slate-400 transition hover:bg-slate-100 hover:text-slate-600 dark:hover:bg-slate-800 dark:hover:text-slate-200"
            :aria-label="t('guide_close')"
            @click="emit('close')"
          >
            <span class="material-symbols-outlined text-[20px]">close</span>
          </button>
        </div>

        <div class="flex flex-wrap gap-2 border-b border-slate-100 px-5 py-3 dark:border-slate-800">
          <button
            v-for="doc in docs"
            :key="doc.id"
            type="button"
            class="rounded-full px-3 py-1.5 text-xs font-semibold transition"
            :class="activeDoc === doc.id ? 'bg-primary text-white' : 'bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700'"
            @click="selectDoc(doc.id)"
          >
            {{ t(doc.labelKey) }}
          </button>
        </div>

        <div class="flex-1 overflow-y-auto px-5 py-4 custom-scrollbar">
          <div v-if="loading" class="rounded-lg border border-dashed border-slate-300 px-4 py-10 text-center text-sm text-slate-500 dark:border-slate-700 dark:text-slate-400">
            {{ t('tutorialDocs_loading') }}
          </div>
          <div v-else-if="error" class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">
            {{ error }}
          </div>
          <article
            v-else
            class="tutorial-markdown max-w-none text-sm leading-7 text-slate-700 dark:text-slate-200"
            v-html="renderedContent"
          ></article>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup>
import { computed, ref, watch } from 'vue';
import { useI18n } from '../i18n';

const props = defineProps({
  open: {
    type: Boolean,
    default: false
  },
  initialDoc: {
    type: String,
    default: 'getting-started'
  }
});

const emit = defineEmits(['close']);

const { t, locale } = useI18n();

const docs = [
  { id: 'getting-started', labelKey: 'tutorialDocs_gettingStarted' },
  { id: 'usage-guide', labelKey: 'tutorialDocs_usageGuide' }
];

const activeDoc = ref(props.initialDoc);
const loading = ref(false);
const error = ref('');
const markdown = ref('');

const tutorialLocale = computed(() => (locale.value === 'zh' ? 'zh_CN' : 'en'));

const currentDocTitle = computed(() => t(docs.find((doc) => doc.id === activeDoc.value)?.labelKey || 'tutorialDocs_gettingStarted'));

const renderedContent = computed(() => formatMarkdown(markdown.value));

watch(
  () => [props.open, props.initialDoc, locale.value],
  ([open, initialDoc]) => {
    if (!open) {
      return;
    }
    activeDoc.value = initialDoc || 'getting-started';
    void loadDoc(activeDoc.value);
  },
  { immediate: true }
);

watch(activeDoc, (docId) => {
  if (props.open) {
    void loadDoc(docId);
  }
});

function selectDoc(docId) {
  activeDoc.value = docId;
}

async function loadDoc(docId) {
  loading.value = true;
  error.value = '';
  markdown.value = '';

  try {
    const response = await fetch(`/api/docs/tutorials?locale=${tutorialLocale.value}&doc=${encodeURIComponent(docId)}`);
    if (!response.ok) {
      throw new Error(t('tutorialDocs_loadFailed'));
    }
    markdown.value = await response.text();
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('tutorialDocs_loadFailed');
  } finally {
    loading.value = false;
  }
}

function escapeHtml(value) {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function formatInlineMarkdown(value) {
  let text = escapeHtml(value);
  text = text.replace(/`([^`]+)`/g, '<code class="rounded bg-slate-100 px-1 py-0.5 text-[12px] dark:bg-slate-800">$1</code>');
  text = text.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a class="text-primary underline" href="$2" target="_blank" rel="noopener noreferrer">$1</a>');
  text = text.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  return text;
}

function formatMarkdown(source) {
  if (!source.trim()) {
    return '';
  }

  const lines = source.replace(/\r\n/g, '\n').split('\n');
  const parts = [];
  let inCodeBlock = false;
  let listType = null;

  const closeList = () => {
    if (listType) {
      parts.push(listType === 'ol' ? '</ol>' : '</ul>');
      listType = null;
    }
  };

  for (const line of lines) {
    if (line.startsWith('```')) {
      closeList();
      if (!inCodeBlock) {
        inCodeBlock = true;
        parts.push('<pre class="my-3 overflow-x-auto rounded-lg bg-slate-950 p-4 text-xs text-slate-100"><code>');
      } else {
        inCodeBlock = false;
        parts.push('</code></pre>');
      }
      continue;
    }

    if (inCodeBlock) {
      parts.push(`${escapeHtml(line)}\n`);
      continue;
    }

    if (!line.trim()) {
      closeList();
      parts.push('<div class="h-3"></div>');
      continue;
    }

    if (line.startsWith('### ')) {
      closeList();
      parts.push(`<h3 class="mt-5 mb-2 text-base font-semibold text-slate-900 dark:text-white">${formatInlineMarkdown(line.slice(4))}</h3>`);
      continue;
    }
    if (line.startsWith('## ')) {
      closeList();
      parts.push(`<h2 class="mt-6 mb-2 text-lg font-semibold text-slate-900 dark:text-white">${formatInlineMarkdown(line.slice(3))}</h2>`);
      continue;
    }
    if (line.startsWith('# ')) {
      closeList();
      parts.push(`<h1 class="mt-2 mb-3 text-xl font-bold text-slate-900 dark:text-white">${formatInlineMarkdown(line.slice(2))}</h1>`);
      continue;
    }

    const orderedMatch = line.match(/^\d+\.\s+(.*)$/);
    if (orderedMatch) {
      if (listType !== 'ol') {
        closeList();
        listType = 'ol';
        parts.push('<ol class="my-2 list-decimal space-y-1 pl-5">');
      }
      parts.push(`<li>${formatInlineMarkdown(orderedMatch[1])}</li>`);
      continue;
    }

    if (line.startsWith('- ') || line.startsWith('* ')) {
      if (listType !== 'ul') {
        closeList();
        listType = 'ul';
        parts.push('<ul class="my-2 list-disc space-y-1 pl-5">');
      }
      parts.push(`<li>${formatInlineMarkdown(line.slice(2))}</li>`);
      continue;
    }

    if (line.startsWith('> ')) {
      closeList();
      parts.push(`<blockquote class="my-3 border-l-4 border-primary/40 bg-primary/5 px-4 py-2 text-slate-600 dark:text-slate-300">${formatInlineMarkdown(line.slice(2))}</blockquote>`);
      continue;
    }

    closeList();
    parts.push(`<p class="my-2">${formatInlineMarkdown(line)}</p>`);
  }

  closeList();
  if (inCodeBlock) {
    parts.push('</code></pre>');
  }

  return parts.join('');
}
</script>
