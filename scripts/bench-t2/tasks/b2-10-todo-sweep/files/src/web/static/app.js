// 前端脚本：搜索框
const box = document.querySelector("#search");

// TODO: P2 - 搜索框加防抖
box.addEventListener("input", () => {
  fetch("/search?q=" + encodeURIComponent(box.value));
});
