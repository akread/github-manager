import globals from 'globals';

export default [
  {
    languageOptions: {
      globals: {
        ...globals.node,
      },
    },
    rules: {
      'no-unused-vars': 'error',
      'no-use-before-define': 'error',
      'no-undef': 'error',
    },
  },
];
