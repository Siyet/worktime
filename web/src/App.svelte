<script lang="ts">
  let now = $state(new Date());

  $effect(() => {
    const interval = setInterval(() => {
      now = new Date();
    }, 1000);
    return () => clearInterval(interval);
  });
</script>

<main>
  <h1>WorkTime</h1>
  <p>Scaffolding is alive: {now.toLocaleTimeString()}</p>
</main>

<style>
  main {
    font-family: system-ui, sans-serif;
    max-width: 40rem;
    margin: 4rem auto;
    padding: 0 1rem;
  }
</style>
