import { ref } from 'vue';

export const isTutorialDocsOpen = ref(false);
export const tutorialDocId = ref('getting-started');

export function openTutorialDocs(doc = 'getting-started') {
  tutorialDocId.value = doc;
  isTutorialDocsOpen.value = true;
}

export function closeTutorialDocs() {
  isTutorialDocsOpen.value = false;
}

export function useTutorialDocs() {
  return {
    isTutorialDocsOpen,
    tutorialDocId,
    openTutorialDocs,
    closeTutorialDocs
  };
}
