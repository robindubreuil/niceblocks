(function () {
  var tooltip, ttHeader, ttWrite, ttRead, ttWriteVal, ttReadVal;

  function init() {
    tooltip = document.getElementById("block-tooltip");
    if (!tooltip) return false;
    ttHeader = document.getElementById("tt-header");
    ttWrite = document.getElementById("tt-write");
    ttRead = document.getElementById("tt-read");
    ttWriteVal = document.getElementById("tt-write-val");
    ttReadVal = document.getElementById("tt-read-val");
    return true;
  }

  function show(e) {
    if (!tooltip && !init()) return;

    var block = e.target.closest("[data-block-idx]");
    if (!block) return;

    var read = block.dataset.readSpeed;
    var write = block.dataset.writeSpeed;
    if (!read && !write) return;

    ttHeader.textContent = "Block #" + block.dataset.blockIdx;

    if (write) {
      ttWriteVal.textContent = "Write: " + write;
      ttWrite.classList.remove("hidden");
      ttWrite.classList.add("flex");
    } else {
      ttWrite.classList.add("hidden");
      ttWrite.classList.remove("flex");
    }

    if (read) {
      ttReadVal.textContent = "Read: " + read;
      ttRead.classList.remove("hidden");
      ttRead.classList.add("flex");
    } else {
      ttRead.classList.add("hidden");
      ttRead.classList.remove("flex");
    }

    tooltip.classList.remove("hidden");

    var rect = block.getBoundingClientRect();
    var w = tooltip.offsetWidth;
    var h = tooltip.offsetHeight;

    var left = rect.left + rect.width / 2 - w / 2;
    var top = rect.top - h - 8;

    if (top < 4) top = rect.bottom + 8;
    if (left < 4) left = 4;
    if (left + w > window.innerWidth - 4)
      left = window.innerWidth - w - 4;

    tooltip.style.left = left + "px";
    tooltip.style.top = top + "px";
  }

  function hide(e) {
    if (!tooltip && !init()) return;
    var block = e.target.closest("[data-block-idx]");
    if (!block) return;
    tooltip.classList.add("hidden");
  }

  document.addEventListener("mouseover", show);
  document.addEventListener("mouseout", hide);
})();
