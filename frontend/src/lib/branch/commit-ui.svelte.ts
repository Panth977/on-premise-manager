/** §21 #104 "Commit…" from any change bar opens the scope's commit dialog (WorkingBar listens). */
export const commitUi = $state({ requests: 0 });
export const requestCommit = () => void commitUi.requests++;
